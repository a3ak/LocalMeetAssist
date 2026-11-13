package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func TestVoiceCalibrationLabels(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Now()
	if err := st.SaveMeeting(model.Meeting{UID: "calibration-meeting", Title: "Calibration", StartedAt: started, Status: "completed", CreatedAt: started, UpdatedAt: started}); err != nil {
		t.Fatal(err)
	}
	speakers := []model.Speaker{
		{ID: "SPEAKER_00", DisplayName: "Иван", NameSource: "manual", PersonID: "person-1", MatchCandidates: []model.SpeakerMatch{{PersonID: "person-1", Score: 0.90}, {PersonID: "person-2", Score: 0.70}}},
		{ID: "SPEAKER_01", DisplayName: "Пётр", NameSource: "manual", PersonID: "person-3", MatchCandidates: []model.SpeakerMatch{{PersonID: "person-3", Score: 0.60}}},
		{ID: "SPEAKER_02", DisplayName: "SPEAKER_02", MatchCandidates: []model.SpeakerMatch{{PersonID: "person-4", Score: 0.65}}},
		{ID: "SPEAKER_03", DisplayName: "Авто", NameSource: "auto", PersonID: "person-5", MatchCandidates: []model.SpeakerMatch{{PersonID: "person-5", Score: 0.95}}},
	}
	for _, speaker := range speakers {
		speaker.MeetingUID = "calibration-meeting"
		if err := st.SaveSpeaker(speaker); err != nil {
			t.Fatal(err)
		}
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/voice/calibration", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("calibration failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response calibrationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Positives != 2 || response.Negatives != 2 {
		t.Fatalf("unexpected labels: %+v", response)
	}
	if response.Auto != 0.90 {
		t.Fatalf("auto threshold=%v, want 0.90", response.Auto)
	}
	if response.Suggest != 0.85 {
		t.Fatalf("suggest threshold=%v, want 0.85", response.Suggest)
	}
	if response.Reliable {
		t.Fatalf("two labels must not be reported as reliable: %+v", response)
	}
}
