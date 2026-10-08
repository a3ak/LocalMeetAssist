package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

const maxTokens = 20

// --- Token management -----------------------------------------------------

// integrationTokens lists tokens (readWrite may list; session manages).
func (s *Server) integrationTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tokens, err := s.store.Tokens()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, tokens)
	case http.MethodPost:
		s.createIntegrationToken(w, r)
	default:
		methodNotAllowed(w)
	}
}

// integrationToken revokes or regenerates a token by ID.
func (s *Server) integrationToken(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/integrations/tokens/"))
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodDelete:
		if err := s.store.DeleteToken(id); err != nil {
			writeError(w, 404, "token not found")
			return
		}
		// A revoked token must not leave live plugin connections behind.
		s.closeBrowserClientsForToken(id)
		writeJSON(w, 200, map[string]any{"ok": true})
	case len(parts) == 2 && parts[1] == "regenerate" && r.Method == http.MethodPost:
		s.regenerateIntegrationToken(w, id)
	default:
		methodNotAllowed(w)
	}
}

// createIntegrationToken creates a read-only or read-write token.
func (s *Server) createIntegrationToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	if req.Kind != tokenKindReadOnly && req.Kind != tokenKindReadWrite {
		writeError(w, 400, "kind must be read_only or read_write")
		return
	}
	var expires *time.Time
	if strings.TrimSpace(req.ExpiresAt) != "" {
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(req.ExpiresAt), time.Local)
		if err != nil {
			writeError(w, 400, "expires_at must be YYYY-MM-DD")
			return
		}
		endOfDay := t.Add(24*time.Hour - time.Second)
		expires = &endOfDay
	}
	tokens, _ := s.store.Tokens()
	if len(tokens) >= maxTokens {
		writeError(w, 400, "token limit reached")
		return
	}
	prefix := "lma_ro_"
	if req.Kind == tokenKindReadWrite {
		prefix = "lma_rw_"
	}
	secret := newTokenSecret(prefix)
	record := buildTokenRecord(uuidv7.New(), req.Name, req.Kind, secret, time.Now(), expires)
	if err := s.store.SaveToken(record); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"token": record, "secret": secret})
}

// regenerateIntegrationToken replaces the secret of an existing token.
func (s *Server) regenerateIntegrationToken(w http.ResponseWriter, id string) {
	tokens, err := s.store.Tokens()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var existing *model.Token
	for i := range tokens {
		if tokens[i].ID == id {
			existing = &tokens[i]
			break
		}
	}
	if existing == nil {
		writeError(w, 404, "token not found")
		return
	}
	prefix := "lma_ro_"
	switch existing.Kind {
	case tokenKindReadWrite:
		prefix = "lma_rw_"
	case tokenKindPlugins:
		prefix = "lma_pl_"
	}
	secret := newTokenSecret(prefix)
	record := buildTokenRecord(existing.ID, existing.Name, existing.Kind, secret, existing.CreatedAt, existing.ExpiresAt)
	if err := s.store.SaveToken(record); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Regenerating closes the connections the old secret authorized.
	s.closeBrowserClientsForToken(record.ID)
	writeJSON(w, 200, map[string]any{"token": record, "secret": secret})
}

// findPluginsToken returns the existing plugins token, or nil.
func (s *Server) findPluginsToken() *model.Token {
	tokens, _ := s.store.Tokens()
	for i := range tokens {
		if tokens[i].Kind == tokenKindPlugins {
			return &tokens[i]
		}
	}
	return nil
}

// ensurePluginsToken returns the single plugins token, creating it if needed.
// The secret is returned only when a new token is created (it cannot be shown
// again afterwards).
func (s *Server) ensurePluginsToken() (*model.Token, string, error) {
	if existing := s.findPluginsToken(); existing != nil {
		return existing, "", nil
	}
	secret := newTokenSecret("lma_pl_")
	// Имя создаётся на языке интерфейса: оно видно в списке токенов, и
	// английская подпись в русском интерфейсе выглядит как непереведённый текст.
	name := "Browser plugin"
	if strings.EqualFold(strings.TrimSpace(s.config().App.Language), "ru") {
		name = "Плагин браузера"
	}
	record := buildTokenRecord(uuidv7.New(), name, tokenKindPlugins, secret, time.Now(), nil)
	if err := s.store.SaveToken(record); err != nil {
		return nil, "", err
	}
	return &record, secret, nil
}

// --- Audit -----------------------------------------------------------------

// integrationAudit returns the tail of the audit log or toggles audit capture.
func (s *Server) integrationAudit(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		writeJSON(w, 200, map[string]any{"enabled": s.config().Integrations.AuditEnabled, "entries": s.readAudit(limit)})
	case http.MethodPut:
		s.setAuditEnabled(w, r)
	default:
		methodNotAllowed(w)
	}
}

// setAuditEnabled turns integration-token audit capture on or off.
func (s *Server) setAuditEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	cfg := s.config()
	if cfg.ConfigFile == "" {
		writeError(w, 500, "configuration file path is unavailable")
		return
	}
	if err := config.UpdateFile(cfg.ConfigFile, map[string]string{"integrations.audit_enabled": strconv.FormatBool(req.Enabled)}); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		writeError(w, 500, "saved but could not be reloaded: "+err.Error())
		return
	}
	loaded.ConfigFile = cfg.ConfigFile
	config.ResolvePaths(&loaded, cfg.ConfigFile)
	s.setConfig(loaded)
	writeJSON(w, 200, map[string]any{"enabled": loaded.Integrations.AuditEnabled})
}

// readAudit returns the last limit JSON audit lines, newest last.
func (s *Server) readAudit(limit int) []map[string]any {
	path := filepath.Join(s.config().Logging.Directory, "integrations-audit.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return []map[string]any{}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

// --- Browser integration ----------------------------------------------------

// integrationBrowser returns or saves the browser recording configuration. This
// endpoint is for the Web UI (session scope); the plugin itself talks over the
// WebSocket at /api/v1/ws.
func (s *Server) integrationBrowser(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.browserConfig(w)
	case http.MethodPut:
		s.saveBrowserConfig(w, r)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) browserConfig(w http.ResponseWriter) {
	cfg := s.config()
	b := cfg.Integrations.Browser
	pluginsToken := s.findPluginsToken()
	patterns := make([]string, 0)
	for _, mask := range parseBrowserMasks(b.Masks) {
		patterns = append(patterns, mask.Pattern)
	}
	bs := s.browser
	bs.mu.Lock()
	scenario := bs.scenario
	owner := bs.owner
	bs.mu.Unlock()
	recording, revision := s.recordingValues()
	writeJSON(w, 200, map[string]any{
		"enabled":                 b.Enabled,
		"masks":                   b.Masks,
		"patterns":                patterns,
		"mode":                    b.Mode,
		"title_template":          b.TitleTemplate,
		"stop_after_missed_polls": b.StopAfterMissedPolls,
		"poll_interval_seconds":   b.PollIntervalSeconds,
		"configured_port":         s.configuredListenPort(),
		"effective_port":          s.effectivePort,
		"plugins_token":           pluginsToken,
		"recording_active":        recording,
		"recording_revision":      revision,
		"state":                   scenario,
		"owner":                   owner != "",
	})
}

// configuredListenPort returns app.listen_port as stored in config.toml. It can
// differ from the running listener until the application is restarted; the
// browser integration uses it to tell the user whether a restart is pending.
func (s *Server) configuredListenPort() int {
	cfg := s.config()
	if cfg.ConfigFile == "" {
		return cfg.App.ListenPort
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		return cfg.App.ListenPort
	}
	return loaded.App.ListenPort
}

func (s *Server) saveBrowserConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled              *bool   `json:"enabled"`
		Masks                *string `json:"masks"`
		Mode                 *string `json:"mode"`
		TitleTemplate        *string `json:"title_template"`
		StopAfterMissedPolls *int    `json:"stop_after_missed_polls"`
		PollIntervalSeconds  *int    `json:"poll_interval_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Mode != nil && *req.Mode != "auto" && *req.Mode != "notify" {
		writeError(w, 400, "mode must be auto or notify")
		return
	}
	if req.StopAfterMissedPolls != nil && (*req.StopAfterMissedPolls < 2 || *req.StopAfterMissedPolls > 10) {
		writeError(w, 400, "stop_after_missed_polls must be between 2 and 10")
		return
	}
	if req.PollIntervalSeconds != nil && (*req.PollIntervalSeconds < 10 || *req.PollIntervalSeconds > 25) {
		writeError(w, 400, "poll_interval_seconds must be between 10 and 25")
		return
	}
	literals := make(map[string]string)
	if req.Enabled != nil {
		literals["integrations.browser.enabled"] = strconv.FormatBool(*req.Enabled)
	}
	if req.Masks != nil {
		literals["integrations.browser.masks"] = strconv.Quote(*req.Masks)
	}
	if req.Mode != nil {
		literals["integrations.browser.mode"] = strconv.Quote(*req.Mode)
	}
	if req.TitleTemplate != nil {
		literals["integrations.browser.title_template"] = strconv.Quote(*req.TitleTemplate)
	}
	if req.StopAfterMissedPolls != nil {
		literals["integrations.browser.stop_after_missed_polls"] = strconv.Itoa(*req.StopAfterMissedPolls)
	}
	if req.PollIntervalSeconds != nil {
		literals["integrations.browser.poll_interval_seconds"] = strconv.Itoa(*req.PollIntervalSeconds)
	}
	cfg := s.config()
	if cfg.ConfigFile == "" {
		writeError(w, 500, "configuration file path is unavailable")
		return
	}
	if err := config.UpdateFile(cfg.ConfigFile, literals); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		writeError(w, 500, "settings saved but could not be reloaded: "+err.Error())
		return
	}
	loaded.ConfigFile = cfg.ConfigFile
	config.ResolvePaths(&loaded, cfg.ConfigFile)
	s.setConfig(loaded)
	var createdSecret, createdID string
	if loaded.Integrations.Browser.Enabled {
		if token, secret, err := s.ensurePluginsToken(); err == nil && token != nil {
			createdSecret, createdID = secret, token.ID
		}
	}
	s.browserOnConfigChanged()
	writeJSON(w, 200, map[string]any{"saved": true, "plugins_token_secret": createdSecret, "plugins_token_id": createdID})
}

// --- Masks and title --------------------------------------------------------

type browserMask struct {
	Name    string
	Pattern string
}

// parseBrowserMasks parses "Name = glob-pattern" lines; comments start with #.
func parseBrowserMasks(raw string) []browserMask {
	var masks []browserMask
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, pattern, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, pattern = strings.TrimSpace(name), strings.TrimSpace(pattern)
		if name == "" || pattern == "" {
			continue
		}
		masks = append(masks, browserMask{Name: name, Pattern: pattern})
	}
	return masks
}

// matchBrowserMask returns the first mask whose glob matches url.
func matchBrowserMask(raw, url string) *browserMask {
	for _, mask := range parseBrowserMasks(raw) {
		if globMatch(mask.Pattern, url) {
			m := mask
			return &m
		}
	}
	return nil
}

// globMatch matches a pattern with '*' wildcards against the whole URL.
func globMatch(pattern, value string) bool {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		if r == '*' {
			b.WriteString(".*")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	matched, err := regexp.MatchString(b.String(), value)
	return err == nil && matched
}

// buildBrowserTitle renders the meeting title from the template.
func (s *Server) buildBrowserTitle(maskName, url string) string {
	cfg := s.config()
	tmpl := cfg.Integrations.Browser.TitleTemplate
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "{{title}} — {{date}} {{time}}"
	}
	now := time.Now()
	replacer := strings.NewReplacer(
		"{{title}}", maskName,
		"{{url}}", url,
		"{{date}}", now.Format(cfg.Storage.DateFormat),
		"{{time}}", now.Format(cfg.Storage.TimeFormat),
		"{{uid}}", uuidv7.New(),
	)
	return strings.TrimSpace(replacer.Replace(tmpl))
}
