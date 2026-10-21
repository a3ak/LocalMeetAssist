package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"localmeetassist/internal/config"
)

// TestTrustedAppDisplayNames keeps the real application name for detection and
// the visible name for meeting titles.
func TestTrustedAppDisplayNames(t *testing.T) {
	cfg := config.Defaults()
	cfg.ConferenceDetection.TrustedApps = []string{"Google Chrome Helper", "Dion Helper"}
	cfg.ConferenceDetection.AppTitles = "Dion Helper = Dion"

	if got := cfg.ConferenceDetection.DisplayName("Dion Helper"); got != "Dion" {
		t.Fatalf("display name = %q, want Dion", got)
	}
	if got := cfg.ConferenceDetection.DisplayName("Google Chrome Helper"); got != "Google Chrome Helper" {
		t.Fatalf("an app without a title must keep its name, got %q", got)
	}
	items := conferenceAppItems(cfg)
	if len(items) != 2 || items[1].Real != "Dion Helper" || items[1].Visible != "Dion" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

// TestParseSettingApps reads the editor format, drops duplicates and falls back
// to the visible name when only one is given.
func TestParseSettingApps(t *testing.T) {
	apps, titles, err := parseSettingApps("Dion Helper = Dion\n\nChrome\nDion Helper = Duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(apps, []string{"Dion Helper", "Chrome"}) {
		t.Fatalf("apps = %+v", apps)
	}
	if !reflect.DeepEqual(titles, []string{"Dion Helper = Dion"}) {
		t.Fatalf("titles = %+v", titles)
	}
	if _, _, err := parseSettingApps(string(bytes.Repeat([]byte("a"), 20*1024))); err == nil {
		t.Fatal("an oversized list must be rejected")
	}
}

// TestSettingsSaveTrustedAppTitles proves the form writes both the trusted list
// and the display names used by the title template.
func TestSettingsSaveTrustedAppTitles(t *testing.T) {
	s := newBrowserServer(t, "auto")
	cfg := s.config()
	cfg.ConfigFile = filepath.Join(t.TempDir(), "config.toml")
	s.setConfig(cfg)

	body, _ := json.Marshal(map[string]any{"values": map[string]string{
		"conference_detection.trusted_apps": "Google Chrome Helper = Chrome\nDion Helper = Dion",
	}})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	s.settings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", recorder.Code, recorder.Body.String())
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.ConferenceDetection.TrustedApps, []string{"Google Chrome Helper", "Dion Helper"}) {
		t.Fatalf("trusted apps = %+v", loaded.ConferenceDetection.TrustedApps)
	}
	if got := loaded.ConferenceDetection.DisplayName("Dion Helper"); got != "Dion" {
		t.Fatalf("display name was not saved: %q", got)
	}
	if got := s.buildConferenceTitle("Dion Helper", "CoreAudio"); got == "" || got[:4] != "Dion" {
		t.Fatalf("the title does not use the visible name: %q", got)
	}
}
