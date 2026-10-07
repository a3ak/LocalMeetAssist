package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
)

// Token kinds stored in the database.
const (
	tokenKindPlugins   = "plugins"
	tokenKindReadOnly  = "read_only"
	tokenKindReadWrite = "read_write"
)

// scope describes what an authenticated request is allowed to do.
type scope int

const (
	scopeNone      scope = iota // no valid credential
	scopeSession   scope = iota // full access (UI cookie / session token)
	scopePlugins   scope = iota // only /api/v1/integrations/browser*
	scopeReadOnly  scope = iota // GET except audio, logs, diagnostics, integrations
	scopeReadWrite scope = iota // everything except token management
)

type authKey struct{}

// authInfo is the resolved identity of a request, attached to the context.
type authInfo struct {
	scope     scope
	tokenID   string
	tokenName string
	tokenKind string
}

// uiNonceState holds the one-time secrets used to hand the browser its cookie.
type uiNonceState struct {
	mu   sync.Mutex
	vals map[string]time.Time
}

// issue creates a one-time nonce that expires after ttl.
func (n *uiNonceState) issue(ttl time.Duration) string {
	nonce := randomToken()
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.vals == nil {
		n.vals = make(map[string]time.Time)
	}
	for k, exp := range n.vals {
		if exp.Before(now) {
			delete(n.vals, k)
		}
	}
	n.vals[nonce] = now.Add(ttl)
	return nonce
}

// consume validates and removes a nonce; it can be used only once.
func (n *uiNonceState) consume(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.vals == nil {
		return false
	}
	exp, ok := n.vals[nonce]
	if !ok || exp.Before(time.Now()) {
		delete(n.vals, nonce)
		return false
	}
	delete(n.vals, nonce)
	return true
}

// newTokenSecret returns a random secret prefixed by prefix for easy
// identification of leaked tokens.
func newTokenSecret(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// hashToken returns the SHA-256 hex digest used for storage and lookup.
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// tokenFingerprint returns the trailing characters shown in the UI.
func tokenFingerprint(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[len(secret)-8:]
}

// buildTokenRecord fills the stored fields of a token from its plaintext.
func buildTokenRecord(id, name, kind, secret string, createdAt time.Time, expiresAt *time.Time) model.Token {
	return model.Token{
		ID:          id,
		Name:        name,
		Kind:        kind,
		Hash:        hashToken(secret),
		Fingerprint: tokenFingerprint(secret),
		CreatedAt:   createdAt,
		ExpiresAt:   expiresAt,
	}
}

// bearerToken extracts the token from Authorization or X-Meeting-Token.
func bearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-Meeting-Token"))
}

// resolveAuth identifies the caller: UI cookie, tray session token or a stored
// integration token.
func (s *Server) resolveAuth(r *http.Request) authInfo {
	if cookie, err := r.Cookie("lma_ui"); err == nil && cookie.Value == s.token {
		return authInfo{scope: scopeSession}
	}
	secret := bearerToken(r)
	if secret == "" {
		return authInfo{scope: scopeNone}
	}
	if secret == s.token {
		return authInfo{scope: scopeSession}
	}
	token, err := s.store.FindTokenByHash(hashToken(secret))
	if err != nil {
		return authInfo{scope: scopeNone}
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return authInfo{scope: scopeNone}
	}
	_ = s.store.TouchTokenLastUsed(token.ID, time.Now())
	info := authInfo{tokenID: token.ID, tokenName: token.Name, tokenKind: token.Kind}
	switch token.Kind {
	case tokenKindPlugins:
		info.scope = scopePlugins
	case tokenKindReadOnly:
		info.scope = scopeReadOnly
	case tokenKindReadWrite:
		info.scope = scopeReadWrite
	}
	return info
}

// requestAuth returns the resolved authInfo of the current request.
func requestAuth(r *http.Request) authInfo {
	if v := r.Context().Value(authKey{}); v != nil {
		return v.(authInfo)
	}
	return authInfo{scope: scopeNone}
}

// authorized reports whether the resolved identity may access this request.
func (s *Server) authorized(r *http.Request, auth authInfo) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/v1/") {
		return true // static assets, index and favicon
	}
	// Public bootstrap endpoints used by the UI before it has a cookie. The
	// WebSocket endpoint authenticates in its hello message, not via headers.
	if path == "/api/v1/health" || path == "/api/v1/config" || path == "/api/v1/ws" {
		return true
	}
	switch auth.scope {
	case scopeSession:
		return true
	case scopePlugins:
		return false // the plugin talks over WebSocket only; no HTTP surface
	case scopeReadOnly:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return false
		}
		return !s.readOnlyDeniedPath(path)
	case scopeReadWrite:
		if isTokenManagementRequest(r, path) {
			return false
		}
		// The integrations admin surface (audit, browser config) is session
		// only; readWrite may list tokens but not manage them.
		if strings.HasPrefix(path, "/api/v1/integrations/") {
			return path == "/api/v1/integrations/tokens" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
		}
		return true
	default:
		return false
	}
}

// readOnlyDeniedPath lists the read endpoints a read-only token may not use.
// Audio artifacts are additionally checked in the download handler where the
// MIME type is known.
func (s *Server) readOnlyDeniedPath(path string) bool {
	if strings.HasPrefix(path, "/api/v1/integrations/") {
		return true
	}
	return path == "/api/v1/logs" || path == "/api/v1/diagnostics"
}

// isTokenManagementRequest reports whether the request creates, revokes or
// regenerates a token. readWrite tokens may list but not manage tokens.
func isTokenManagementRequest(r *http.Request, path string) bool {
	if !strings.HasPrefix(path, "/api/v1/integrations/tokens") {
		return false
	}
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// maybeAudit appends a usage line for integration-token requests.
func (s *Server) maybeAudit(sw *statusWriter, r *http.Request, auth authInfo, started time.Time) {
	if auth.scope != scopePlugins && auth.scope != scopeReadOnly && auth.scope != scopeReadWrite {
		return
	}
	if !s.config().Integrations.AuditEnabled {
		return
	}
	entry := map[string]any{
		"time":        started.UTC().Format(time.RFC3339Nano),
		"token":       auth.tokenName,
		"kind":        auth.tokenKind,
		"ip":          remoteIP(r),
		"user_agent":  r.UserAgent(),
		"method":      r.Method,
		"path":        r.URL.Path,
		"status":      sw.status,
		"duration_ms": time.Since(started).Milliseconds(),
	}
	s.appendAudit(entry)
}

// appendAudit writes one JSON line to the audit log file.
func (s *Server) appendAudit(entry map[string]any) {
	dir := s.config().Logging.Directory
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "integrations-audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// remoteIP returns the host part of RemoteAddr (always loopback in practice).
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// logRequest writes the standard access log line for a completed request.
func (s *Server) logRequest(sw *statusWriter, r *http.Request, started time.Time) {
	duration := time.Since(started).Round(time.Millisecond)
	switch {
	case sw.status >= 500:
		appLogging.Errorf(s.logger, "http method=%s path=%s status=%d duration=%s", r.Method, r.URL.Path, sw.status, duration)
	case sw.status >= 400:
		appLogging.Warnf(s.logger, "http method=%s path=%s status=%d duration=%s", r.Method, r.URL.Path, sw.status, duration)
	default:
		appLogging.Debugf(s.logger, "http method=%s path=%s status=%d duration=%s", r.Method, r.URL.Path, sw.status, duration)
	}
}
