// Package server exposes the local HTTP API and serves the embedded Web UI.
package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"localmeetassist/internal/config"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
	"localmeetassist/internal/modelmanager"
	"localmeetassist/internal/pipeline"
	"localmeetassist/internal/recording"
	"localmeetassist/internal/store"
	"localmeetassist/internal/version"
)

//go:embed web
var webFS embed.FS

// Server holds the application state shared by all HTTP handlers.
type Server struct {
	cfgMu         sync.RWMutex
	cfg           config.Config
	store         *store.Store
	recorder      *recording.Manager
	pipeline      *pipeline.Runner
	models        *modelmanager.Manager
	token         string
	nonces        *uiNonceState
	effectivePort int
	browser       *browserSession
	recording     *recordingTracker
	logger        *log.Logger
	logLevel      *appLogging.Controller
	eventsMu      sync.Mutex
	events        map[chan []byte]struct{}
}

func (s *Server) config() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Server) setConfig(cfg config.Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

// CurrentConfig returns a copy for desktop integrations such as tray hotkey
// reloading. Mutable server internals remain protected by cfgMu.
func (s *Server) CurrentConfig() config.Config { return s.config() }

// SessionToken authorizes local state-changing API requests made by the tray.
func (s *Server) SessionToken() string { return s.token }

// ownerName returns the configured microphone owner name, or the localized
// default when app.microphone_owner_name is empty. Defaults are resolved when
// a meeting is created, so stored data keeps the language of that moment.
func (s *Server) ownerName() string {
	cfg := s.config()
	if name := strings.TrimSpace(cfg.App.MicrophoneOwnerName); name != "" {
		return name
	}
	return config.DefaultMicrophoneOwnerName(cfg.App.Language)
}

// New creates a Server backed by a native recording manager.
func New(cfg config.Config, st *store.Store, logger *log.Logger) *Server {
	return NewWithRecorder(cfg, st, logger, recording.NewManager(cfg.Audio, logger))
}

// NewWithLogging creates a Server with a runtime log-level controller.
func NewWithLogging(cfg config.Config, st *store.Store, logger *log.Logger, level *appLogging.Controller) *Server {
	s := NewWithRecorder(cfg, st, logger, recording.NewManager(cfg.Audio, logger))
	s.logLevel = level
	return s
}

// NewWithRecorder creates a Server with an injected recorder (used by tests).
func NewWithRecorder(cfg config.Config, st *store.Store, logger *log.Logger, recorder *recording.Manager) *Server {
	s := &Server{cfg: cfg, store: st, recorder: recorder, pipeline: pipeline.NewWithLogger(cfg, st, logger), models: modelmanager.New(cfg, logger), token: randomToken(), nonces: &uiNonceState{}, browser: newBrowserSession(), recording: &recordingTracker{}, logger: logger, events: make(map[chan []byte]struct{})}
	// A recording cannot survive a restart, so the persisted revision has to be
	// reconciled before the first plugin connects.
	s.normaliseRecordingState()
	return s
}

// PrepareInference starts optional background downloads of the native runtimes
// selected by the current configuration. It is called by main, not by New, so
// constructing a Server in tests never performs network I/O.
func (s *Server) PrepareInference() {
	cfg := s.config()
	if !cfg.Inference.AutoDownload {
		return
	}
	for _, status := range s.models.Statuses() {
		needed := status.ID == "onnx-runtime" ||
			(status.ID == "whisper-runtime" && strings.EqualFold(cfg.Transcription.Engine, "whispercpp-native"))
		if needed && !status.Exists && !status.Downloading {
			if err := s.models.Start(status.ID); err != nil {
				appLogging.Errorf(s.logger, "inference runtime automatic download not started id=%s error=%v", status.ID, err)
			} else {
				s.logger.Printf("inference runtime automatic download started id=%s path=%q", status.ID, status.Path)
			}
		}
	}
}

// Listen binds the loopback listener on the configured host and port.
func (s *Server) Listen() (net.Listener, error) {
	cfg := s.config()
	if cfg.App.ListenHost != "127.0.0.1" && cfg.App.ListenHost != "localhost" && cfg.App.ListenHost != "::1" {
		return nil, errors.New("listen_host must be a loopback address")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.App.ListenHost, strconv.Itoa(cfg.App.ListenPort)))
	if err != nil {
		return nil, err
	}
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		s.effectivePort = addr.Port
	}
	return ln, nil
}

// baseURL returns the loopback URL of the running server.
func (s *Server) baseURL() string {
	cfg := s.config()
	host := cfg.App.ListenHost
	if host == "::1" {
		host = "[::1]"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.effectivePort)) + "/"
}

// IssueUIURL returns a one-time URL that grants the browser its session cookie.
func (s *Server) IssueUIURL() string {
	return s.baseURL() + "?ui=" + s.nonces.issue(2*time.Minute)
}

// Handler builds the HTTP router with the security middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/config", s.publicConfig)
	mux.HandleFunc("/api/v1/settings", s.settings)
	mux.HandleFunc("/api/v1/audio/devices", s.audioDevices)
	mux.HandleFunc("/api/v1/diagnostics", s.diagnostics)
	mux.HandleFunc("/api/v1/logs", s.logs)
	mux.HandleFunc("/api/v1/search", s.searchMeetings)
	mux.HandleFunc("/api/v1/models", s.modelList)
	mux.HandleFunc("/api/v1/models/", s.modelPath)
	mux.HandleFunc("/api/v1/meetings", s.meetings)
	mux.HandleFunc("/api/v1/meetings/", s.meetingPath)
	mux.HandleFunc("/api/v1/recordings", s.recordings)
	mux.HandleFunc("/api/v1/recordings/", s.recordingPath)
	mux.HandleFunc("/api/v1/events", s.eventStream)
	mux.HandleFunc("/api/v1/integrations/tokens", s.integrationTokens)
	mux.HandleFunc("/api/v1/integrations/tokens/", s.integrationToken)
	mux.HandleFunc("/api/v1/integrations/audit", s.integrationAudit)
	mux.HandleFunc("/api/v1/integrations/browser", s.integrationBrowser)
	mux.HandleFunc("/api/v1/ws", s.websocketHandler)
	return s.withSecurity(mux)
}

// RecoverProcessing restarts pipelines that were interrupted by an application
// restart. Only finalized recordings with both source WAV files are resumed;
// stale rows without recoverable audio become an explicit warning rather than
// making the UI poll forever.
func (s *Server) RecoverProcessing() int {
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		appLogging.Errorf(s.logger, "pipeline recovery list failed: %v", err)
		return 0
	}
	recovered := 0
	for _, meeting := range meetings {
		if meeting.Status != "processing" || s.pipeline.IsRunning(meeting.UID) {
			continue
		}
		audioDir := filepath.Join(s.meetingDir(meeting), "audio")
		if !validWAVFile(filepath.Join(audioDir, "microphone.wav")) || !validWAVFile(filepath.Join(audioDir, "system.wav")) {
			meeting.Status = "warning"
			meeting.ProcessingStage = "interrupted"
			meeting.LastError = "Processing was interrupted by a restart and the source WAV files are unavailable. Re-run processing after attaching audio."
			meeting.UpdatedAt = time.Now()
			_ = s.store.SaveMeeting(meeting)
			continue
		}
		meeting.ProcessingStage = "recovered"
		meeting.LastError = ""
		meeting.UpdatedAt = time.Now()
		_ = s.store.SaveMeeting(meeting)
		if startErr := s.pipeline.Start(meeting.UID); startErr != nil {
			appLogging.Errorf(s.logger, "pipeline recovery failed uid=%s error=%v", meeting.UID, startErr)
			continue
		}
		recovered++
		s.logger.Printf("pipeline recovered uid=%s", meeting.UID)
	}
	return recovered
}

func validWAVFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() >= 44
}

func (s *Server) withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		slowTimer := time.AfterFunc(10*time.Second, func() {
			// The SSE stream stays open for the whole session; it is not a
			// slow request and must not pollute the log.
			if r.URL.Path == "/api/v1/events" || r.URL.Path == "/api/v1/ws" {
				return
			}
			appLogging.Warnf(s.logger, "http request still running method=%s path=%s elapsed=%s", r.Method, r.URL.Path, time.Since(started).Round(time.Second))
		})
		defer slowTimer.Stop()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; media-src 'self' blob:; connect-src 'self'")
		auth := s.resolveAuth(r)
		if s.authorized(r, auth) {
			ctx := context.WithValue(r.Context(), authKey{}, auth)
			next.ServeHTTP(sw, r.WithContext(ctx))
		} else {
			writeError(sw, http.StatusForbidden, "forbidden")
		}
		s.logRequest(sw, r, started)
		s.maybeAudit(sw, r, auth, started)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		// Exchange a one-time UI nonce for the session cookie, then drop the
		// secret from the URL so it never reaches history or the Referer.
		if nonce := r.URL.Query().Get("ui"); nonce != "" {
			if s.nonces.consume(nonce) {
				http.SetCookie(w, &http.Cookie{Name: "lma_ui", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		data, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	if r.URL.Path == "/favicon.ico" {
		// Browsers request /favicon.ico by default; serve the embedded SVG so
		// the tab icon works and the request is not logged as a 404.
		if data, err := webFS.ReadFile("web/favicon.svg"); err == nil {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/web/") {
		w.Header().Set("Cache-Control", "no-store")
		sub, _ := fs.Sub(webFS, "web")
		http.StripPrefix("/web/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.Version, "go": runtime.Version(), "recording_backend": cfg.Audio.Backend})
}
func (s *Server) publicConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	writeJSON(w, http.StatusOK, map[string]any{"language": cfg.App.Language, "listen_port": s.effectivePort, "sample_rate": cfg.Audio.SampleRate, "channels": cfg.Audio.Channels, "backend": cfg.Audio.Backend, "input_device_id": cfg.Audio.InputDeviceID, "input_device_name": cfg.Audio.InputDeviceName, "output_device_id": cfg.Audio.OutputDeviceID, "output_device_name": cfg.Audio.OutputDeviceName, "transcription_auto_run": cfg.Transcription.AutoRun, "transcription_engine": cfg.Transcription.Engine, "transcription_source_mode": cfg.Transcription.SourceMode, "diarization_auto_run": cfg.Diarization.AutoRun, "diarization_engine": cfg.Diarization.Engine, "echo_dedup_enabled": cfg.Transcription.EchoDedupEnabled, "mixed_fallback_enabled": cfg.Transcription.MixedFallbackEnabled, "summary_auto_run": cfg.Summary.AutoRun, "audio_after_processing": cfg.Storage.AudioAfterProcessing})
}

func (s *Server) audioDevices(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	appLogging.Debugf(s.logger, "audio device scan started")
	report := s.recorder.Devices(ctx)
	if !report.Available {
		appLogging.Warnf(s.logger, "audio device scan unavailable warnings=%q", report.Warnings)
	} else {
		appLogging.Debugf(s.logger, "audio device scan completed microphones=%d system_sources=%d", len(report.Microphones), len(report.SystemSources))
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) diagnostics(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	appLogging.Debugf(s.logger, "diagnostics collection started")
	report := s.recorder.Devices(ctx)
	if !report.Available {
		appLogging.Warnf(s.logger, "diagnostics audio probe unavailable warnings=%q", report.Warnings)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version.Version, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"data_dir": cfg.App.DataDir, "database_path": cfg.Storage.DatabasePath,
		"log_path":     filepath.Join(cfg.Logging.Directory, "localmeetassist.log"),
		"audio":        report,
		"onnx_runtime": fileDiagnostic(modelmanager.ResolveRuntimeLibrary(cfg.Inference.RuntimePath, cfg.Inference.RuntimeVersion)),
		"transcription": map[string]any{
			"auto_run":           cfg.Transcription.AutoRun,
			"engine":             cfg.Transcription.Engine,
			"model":              fileDiagnostic(cfg.Transcription.ModelPath),
			"echo_dedup_enabled": cfg.Transcription.EchoDedupEnabled,
		},
		"diarization": map[string]any{
			"auto_run":           cfg.Diarization.AutoRun,
			"engine":             cfg.Diarization.Engine,
			"segmentation_model": fileDiagnostic(cfg.Diarization.SegmentationModel),
			"embedding_model":    fileDiagnostic(cfg.Diarization.EmbeddingModel),
		},
		"summary_auto_run": cfg.Summary.AutoRun,
	})
}

func fileDiagnostic(path string) map[string]any {
	result := map[string]any{"path": path, "exists": false}
	if strings.TrimSpace(path) == "" {
		return result
	}
	info, err := os.Stat(path)
	if err == nil {
		result["exists"] = true
		result["size_bytes"] = info.Size()
	} else {
		result["error"] = err.Error()
	}
	return result
}

func executableDiagnostic(path string) map[string]any {
	result := fileDiagnostic(path)
	if result["exists"] == true || runtime.GOOS != "windows" || filepath.Ext(path) != "" {
		return result
	}
	windowsPath := path + ".exe"
	windowsResult := fileDiagnostic(windowsPath)
	if windowsResult["exists"] == true {
		return windowsResult
	}
	return result
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 && value <= 1000 {
		limit = value
	}
	cfg := s.config()
	lines, err := tailLines(filepath.Join(cfg.Logging.Directory, "localmeetassist.log"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}
func (s *Server) meetingDir(m model.Meeting) string {
	cfg := s.config()
	return filepath.Join(cfg.App.DataDir, "meetings", m.StartedAt.Format("2006"), m.StartedAt.Format("01"), m.UID)
}
func (s *Server) safeManagedPath(p string) bool {
	cfg := s.config()
	base, _ := filepath.Abs(cfg.App.DataDir)
	target, _ := filepath.Abs(p)
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func splitPath(v string) []string {
	var out []string
	for _, p := range strings.Split(strings.Trim(v, "/"), "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
func randomToken() string { b := make([]byte, 32); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status code and forwards it.
func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write forwards the body unchanged.
func (w *statusWriter) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

// Flush forwards to the underlying response when it can flush.
func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ReadFrom delegates to the underlying writer so http.ServeContent can use
// sendfile/copy_file_range for large artifact downloads instead of falling
// back to a 32 KiB buffer loop. The fallback wraps w in an io.Writer-only view
// to avoid re-entering this method through io.Copy.
func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

func tailLines(path string, limit int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	if lines == nil {
		lines = []string{}
	}
	return lines, nil
}
