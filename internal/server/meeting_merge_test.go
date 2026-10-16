package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/nativeaudio"
	"localmeetassist/internal/store"
)

func TestMergeMeetingsPreservesWallClockTimeline(t *testing.T) {
	s, handler := newIntegrationServer(t)
	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local)
	meetings := []model.Meeting{
		{UID: "merge-source-a", Title: "Reconnect test", StartedAt: start, DurationMS: 1000, Status: "completed", Source: "recorded", CreatedAt: start, UpdatedAt: start},
		{UID: "merge-source-b", Title: "Reconnect test", StartedAt: start.Add(3 * time.Second), DurationMS: 1000, Status: "completed", Source: "recorded", CreatedAt: start, UpdatedAt: start},
	}
	for index, meeting := range meetings {
		finished := meeting.StartedAt.Add(time.Second)
		meeting.FinishedAt = &finished
		if err := s.store.SaveMeeting(meeting); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(s.meetingDir(meeting), "audio")
		for _, name := range []string{"microphone.wav", "system.wav"} {
			path := filepath.Join(dir, name)
			writer, err := audio.NewWAVWriter(path, 16000, 1)
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte{byte(index + 1), 0}, 16000)
			if err := writer.WritePCM16(data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.store.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Антон", Source: "system"}); err != nil {
			t.Fatal(err)
		}
		if err := s.store.SaveTranscriptSegments(meeting.UID, []model.Segment{{ID: "line", StartMS: 0, EndMS: 500, SpeakerID: "SPEAKER_00", Source: "system", Text: "Фрагмент"}}); err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := json.Marshal(map[string]any{"meeting_uids": []string{meetings[1].UID, meetings[0].UID}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/merge", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("merge: %d %s", rr.Code, rr.Body.String())
	}
	var merged model.Meeting
	if err := json.Unmarshal(rr.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if merged.DurationMS != 4000 {
		t.Fatalf("duration=%d want=4000", merged.DurationMS)
	}
	if !bytes.Contains([]byte(merged.Transcript), []byte("[00:03] Антон: Фрагмент")) {
		t.Fatalf("transcript timeline:\n%s", merged.Transcript)
	}
	if _, err := s.store.Meeting(meetings[0].UID); !os.IsNotExist(err) {
		t.Fatalf("source meeting still exists: %v", err)
	}
	mic := filepath.Join(s.meetingDir(merged), "audio", "microphone.wav")
	if duration, err := audio.PCM16WAVDurationMS(mic); err != nil || duration != 4000 {
		t.Fatalf("audio duration=%d err=%v", duration, err)
	}
}

type fixedConferenceActivity struct{ value nativeaudio.Activity }

func (f fixedConferenceActivity) Activity() nativeaudio.Activity { return f.value }

// conferenceCandidateOf returns the current candidate as a typed pointer. The
// snapshot map stores it as interface{}, so comparing the map value against nil
// is always false: a typed nil pointer is a non-nil interface.
func conferenceCandidateOf(s *Server) *conferenceCandidate {
	value, _ := s.conferenceSnapshot()["candidate"].(*conferenceCandidate)
	return value
}

func TestConferenceDetectionRequiresConfiguredWindowAndResets(t *testing.T) {
	cfg := config.Defaults()
	cfg.ConferenceDetection.Enabled = true
	cfg.ConferenceDetection.WindowSeconds = 10
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(os.Stderr, "", 0))
	s.activity = fixedConferenceActivity{nativeaudio.Activity{Microphone: true, System: true, Available: true, Backend: "test"}}
	base := time.Now()
	s.observeConferenceActivity(base)
	s.observeConferenceActivity(base.Add(9 * time.Second))
	if conferenceCandidateOf(s) != nil {
		t.Fatal("candidate appeared before the window elapsed")
	}
	s.observeConferenceActivity(base.Add(10 * time.Second))
	if conferenceCandidateOf(s) == nil {
		t.Fatal("candidate was not created")
	}
	s.activity = fixedConferenceActivity{nativeaudio.Activity{Available: true, Backend: "test"}}
	s.observeConferenceActivity(base.Add(11 * time.Second))
	if conferenceCandidateOf(s) != nil {
		t.Fatal("candidate did not reset after activity ended")
	}
}

func TestConferenceDetectionClassifiesApps(t *testing.T) {
	cfg := config.Defaults()
	cfg.ConferenceDetection.Enabled = true
	cfg.ConferenceDetection.WindowSeconds = 1
	cfg.ConferenceDetection.TrustedApps = []string{"Zoom Workplace"}
	cfg.ConferenceDetection.ExcludedApps = []string{"Voice Memos"}
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(os.Stderr, "", 0))

	// Доверенное приложение — кандидат сразу со знаком доверия и именем.
	s.activity = fixedConferenceActivity{nativeaudio.Activity{Microphone: true, System: true, Available: true, Backend: "test", MicrophoneApp: "Zoom Workplace"}}
	base := time.Now()
	s.observeConferenceActivity(base)
	s.observeConferenceActivity(base.Add(time.Second))
	candidate := conferenceCandidateOf(s)
	if candidate == nil || !candidate.Trusted || candidate.AppName != "Zoom Workplace" {
		t.Fatalf("trusted app candidate mismatch: %+v", candidate)
	}

	// Исключённое приложение кандидата не создаёт.
	s.resetConferenceDetection(false)
	s.activity = fixedConferenceActivity{nativeaudio.Activity{Microphone: true, System: true, Available: true, Backend: "test", MicrophoneApp: "Voice Memos"}}
	s.observeConferenceActivity(base.Add(2 * time.Second))
	s.observeConferenceActivity(base.Add(3 * time.Second))
	if candidate := conferenceCandidateOf(s); candidate != nil {
		t.Fatalf("excluded app must not produce a candidate: %+v", candidate)
	}

	// Неизвестное приложение — кандидат без знака доверия.
	s.resetConferenceDetection(false)
	s.activity = fixedConferenceActivity{nativeaudio.Activity{Microphone: true, System: true, Available: true, Backend: "test", MicrophoneApp: "SomeNewApp"}}
	s.observeConferenceActivity(base.Add(4 * time.Second))
	s.observeConferenceActivity(base.Add(5 * time.Second))
	candidate = conferenceCandidateOf(s)
	if candidate == nil || candidate.Trusted || candidate.AppName != "SomeNewApp" {
		t.Fatalf("unknown app candidate mismatch: %+v", candidate)
	}
}
