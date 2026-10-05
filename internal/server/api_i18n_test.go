package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func newI18nTestServer(t *testing.T, language string) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.Language = language
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveMeeting(model.Meeting{UID: "i18n-meeting", Title: "probe", StartedAt: time.Now(), Status: "created"}); err != nil {
		t.Fatal(err)
	}
	return New(cfg, st, log.New(io.Discard, "", 0))
}

func retryUnknownStage(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/i18n-meeting/jobs/bogus/retry", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

// TestAPIErrorsAreAlwaysEnglish pins the decision that code-level errors are
// not localized: the message stays in simple English for any interface
// language, because maintaining translations for every error is too costly.
func TestAPIErrorsAreAlwaysEnglish(t *testing.T) {
	for _, language := range []string{"en", "ru"} {
		rr := retryUnknownStage(t, newI18nTestServer(t, language))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("lang=%s unexpected status %d: %s", language, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "unknown processing stage") {
			t.Fatalf("lang=%s error is not English: %s", language, rr.Body.String())
		}
		if hasCyrillic(rr.Body.String()) {
			t.Fatalf("lang=%s error still contains Cyrillic: %s", language, rr.Body.String())
		}
	}
}
