package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localmeetassist/internal/config"
	"localmeetassist/internal/recording"
	"localmeetassist/internal/store"
)

func newIntegrationServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(cfg, st, log.New(io.Discard, "", 0))
	return s, s.Handler()
}

// newBrowserServer builds a server whose recorder uses the synthetic backend, so
// recording start/stop works without real audio devices.
func newBrowserServer(t *testing.T, mode string) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Audio.InputDeviceID = "mic"
	cfg.Audio.InputDeviceName = "Test microphone"
	cfg.Audio.OutputDeviceID = "system"
	cfg.Audio.OutputDeviceName = "Test system"
	cfg.Transcription.AutoRun = false
	cfg.Summary.AutoRun = false
	cfg.Integrations.Browser.Enabled = true
	cfg.Integrations.Browser.Mode = mode
	cfg.Integrations.Browser.Masks = "Zoom = *zoom.us/*"
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	recorder := recording.NewManagerWithBackend(cfg.Audio, log.New(io.Discard, "", 0), serverSyntheticBackend{})
	return NewWithRecorder(cfg, st, log.New(io.Discard, "", 0), recorder)
}

func createToken(t *testing.T, h http.Handler, s *Server, kind, name string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "kind": kind})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create token failed: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token  map[string]any `json:"token"`
		Secret string         `json:"secret"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Token["id"].(string), resp.Secret
}

func TestTokenScopes(t *testing.T) {
	s, h := newIntegrationServer(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated GET must be forbidden, got %d", rr.Code)
	}

	_, roSecret := createToken(t, h, s, tokenKindReadOnly, "RO")
	_, rwSecret := createToken(t, h, s, tokenKindReadWrite, "RW")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
	req.Header.Set("Authorization", "Bearer "+roSecret)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("readOnly GET meetings: %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	req.Header.Set("Authorization", "Bearer "+roSecret)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("readOnly GET logs must be forbidden, got %d", rr.Code)
	}
	body, _ := json.Marshal(map[string]string{"title": "x"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/meetings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+roSecret)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("readOnly POST must be forbidden, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/integrations/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+rwSecret)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("readWrite list tokens: %d", rr.Code)
	}
	body, _ = json.Marshal(map[string]string{"name": "X", "kind": tokenKindReadOnly})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/integrations/tokens", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rwSecret)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("readWrite create token must be forbidden, got %d", rr.Code)
	}
}

func TestTokenExpiry(t *testing.T) {
	s, h := newIntegrationServer(t)
	body, _ := json.Marshal(map[string]string{"name": "expired", "kind": tokenKindReadOnly, "expires_at": "2000-01-01"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create expired token failed: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Secret)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expired token must be rejected, got %d", rr.Code)
	}
}

func TestUINonceSetsSessionCookie(t *testing.T) {
	s, h := newIntegrationServer(t)
	u := s.IssueUIURL()
	nonce := u[strings.LastIndex(u, "=")+1:]

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/?ui="+nonce, nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Set-Cookie"), "lma_ui="+s.token) {
		t.Fatalf("session cookie not set: %v", rr.Header())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/?ui="+nonce, nil))
	if strings.Contains(rr.Header().Get("Set-Cookie"), "lma_ui=") {
		t.Fatalf("nonce must not be reusable")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
	req.AddCookie(&http.Cookie{Name: "lma_ui", Value: s.token})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cookie auth failed: %d", rr.Code)
	}
}

func TestBrowserConfigCreatesPluginsToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("config_version = 1\n[app]\ndata_dir = \"./data\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{
		"enabled": true, "masks": "Zoom = *.zoom.us/wc/*", "mode": "auto",
		"title_template": "{{title}} — {{date}} {{time}}", "stop_after_missed_polls": 4, "poll_interval_seconds": 20,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/integrations/browser", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save browser config failed: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		PluginsTokenSecret string `json:"plugins_token_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.PluginsTokenSecret == "" {
		t.Fatalf("expected plugins token secret, got %s", rr.Body.String())
	}
	// The plugins token has no HTTP surface anymore: listing tokens is forbidden.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/integrations/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+resp.PluginsTokenSecret)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("plugins token must not list tokens, got %d", rr.Code)
	}
}

func TestParseBrowserMasksAndGlob(t *testing.T) {
	masks := parseBrowserMasks("# comment\nZoom = *.zoom.us/wc/*\n\nGoogle Meet = meet.google.com/*")
	if len(masks) != 2 {
		t.Fatalf("expected 2 masks, got %d", len(masks))
	}
	if masks[0].Name != "Zoom" || masks[0].Pattern != "*.zoom.us/wc/*" {
		t.Fatalf("unexpected first mask: %+v", masks[0])
	}
	if !globMatch("*zoom.us/*", "https://zoom.us/wc/join") {
		t.Fatal("glob should match")
	}
	if globMatch("*zoom.us/*", "https://meet.google.com/abc") {
		t.Fatal("glob should not match")
	}
	if !globMatch("*example.com/*", "https://EXAMPLE.com/path") {
		t.Fatal("glob should match case-insensitively")
	}
}

func TestBrowserConfigRanges(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("config_version = 1\n[app]\ndata_dir = \"./data\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()

	if got := s.config().Integrations.Browser.StopAfterMissedPolls; got != 4 {
		t.Fatalf("default stop_after_missed_polls = %d, want 4", got)
	}
	if got := s.config().Integrations.Browser.PollIntervalSeconds; got != 20 {
		t.Fatalf("default poll_interval_seconds = %d, want 20", got)
	}

	put := func(body map[string]any) int {
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/integrations/browser", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Meeting-Token", s.token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := put(map[string]any{"stop_after_missed_polls": 10}); code != http.StatusOK {
		t.Fatalf("stop_after_missed_polls=10 rejected: %d", code)
	}
	if code := put(map[string]any{"stop_after_missed_polls": 11}); code != http.StatusBadRequest {
		t.Fatalf("stop_after_missed_polls=11 must be rejected, got %d", code)
	}
	if code := put(map[string]any{"stop_after_missed_polls": 1}); code != http.StatusBadRequest {
		t.Fatalf("stop_after_missed_polls=1 must be rejected, got %d", code)
	}
	if code := put(map[string]any{"poll_interval_seconds": 10}); code != http.StatusOK {
		t.Fatalf("poll_interval_seconds=10 rejected: %d", code)
	}
	if code := put(map[string]any{"poll_interval_seconds": 25}); code != http.StatusOK {
		t.Fatalf("poll_interval_seconds=25 rejected: %d", code)
	}
	if code := put(map[string]any{"poll_interval_seconds": 9}); code != http.StatusBadRequest {
		t.Fatalf("poll_interval_seconds=9 must be rejected, got %d", code)
	}
	if code := put(map[string]any{"poll_interval_seconds": 26}); code != http.StatusBadRequest {
		t.Fatalf("poll_interval_seconds=26 must be rejected, got %d", code)
	}
}

// --- WebSocket state machine moved to browser_session_test.go -----------------

// --- WebSocket transport helpers moved to browser_session_test.go -------------

// TestBrowserConfigReportsPendingPortRestart guards the three-state port warning:
// after pinning app.listen_port the UI must be able to tell that the running
// listener still uses another port.
func TestBrowserConfigReportsPendingPortRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("config_version = 1\n[app]\ndata_dir = \"./data\"\nlisten_port = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{"values": map[string]string{"app.listen_port": "9123"}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save settings: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/integrations/browser", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("browser config: %d", rr.Code)
	}
	var resp struct {
		ConfiguredPort int `json:"configured_port"`
		EffectivePort  int `json:"effective_port"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ConfiguredPort != 9123 {
		t.Fatalf("configured_port = %d, want 9123 (value stored in config.toml)", resp.ConfiguredPort)
	}
	if resp.EffectivePort != 0 {
		t.Fatalf("effective_port = %d, want 0 (no listener is bound in this test)", resp.EffectivePort)
	}
}

// WebSocket handshake tests moved to browser_session_test.go.
