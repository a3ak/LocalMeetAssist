package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
)

// TestSettingsSavedDuringRecordingApplyOnNextMeeting keeps the running meeting on
// the settings it started with: the file is updated at once, but the process
// picks the values up only when nothing is recorded or processed.
func TestSettingsSavedDuringRecordingApplyOnNextMeeting(t *testing.T) {
	s := newBrowserServer(t, "auto")
	dir := t.TempDir()
	cfg := s.config()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	s.setConfig(cfg)

	meeting, err := s.startMeetingRecording(context.Background(), model.RecordingStart{}, "recorded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.recorder.AnyActive() {
		t.Fatal("the test needs an active recording")
	}

	body := bytes.NewBufferString(`{"values":{"transcription.threads":"5"}}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", body)
	recorder := httptest.NewRecorder()
	s.settings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("saving during a recording failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["deferred"] != true {
		t.Fatalf("the save must be deferred, got %v", response)
	}
	if s.config().Transcription.Threads == 5 {
		t.Fatal("the running process applied the new setting immediately")
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Transcription.Threads != 5 {
		t.Fatalf("the setting was not written to config.toml: %d", loaded.Transcription.Threads)
	}

	// Следующая встреча стартует уже с новыми значениями.
	if _, err := s.recorder.Stop(meeting.UID); err != nil {
		t.Fatal(err)
	}
	next, err := s.startMeetingRecording(context.Background(), model.RecordingStart{}, "recorded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.recorder.Stop(next.UID) }()
	if got := s.config().Transcription.Threads; got != 5 {
		t.Fatalf("the next meeting did not apply the deferred settings: %d", got)
	}
}
