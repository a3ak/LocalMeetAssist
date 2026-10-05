package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGeneratedDefaultsFollowInterfaceLanguage pins that the default owner name
// is resolved in the interface language when a meeting is created.
func TestGeneratedDefaultsFollowInterfaceLanguage(t *testing.T) {
	if got := newI18nTestServer(t, "en").ownerName(); got != "Microphone owner" {
		t.Fatalf("english default owner name = %q", got)
	}
	if got := newI18nTestServer(t, "ru").ownerName(); got != "Владелец микрофона" {
		t.Fatalf("russian default owner name = %q", got)
	}
}

// TestManualMeetingTitleFollowsInterfaceLanguage checks the placeholder title of
// a meeting created without a name.
func TestManualMeetingTitleFollowsInterfaceLanguage(t *testing.T) {
	s := newI18nTestServer(t, "en")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings", strings.NewReader("{}"))
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Meeting ") {
		t.Fatalf("meeting title is not localized: %s", rr.Body.String())
	}
}
