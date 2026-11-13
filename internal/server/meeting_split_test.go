package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
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
	"localmeetassist/internal/store"
)

func TestSplitMeetingKeepsTranscriptAndSpeakerLinks(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Now().Add(-3 * time.Minute)
	finished := started.Add(3 * time.Minute)
	meeting := model.Meeting{
		UID: "meeting-split", Title: "Split me", StartedAt: started, FinishedAt: &finished,
		DurationMS: 180000, Status: "completed", Source: "recorded", Summary: "old summary",
		SummaryStatus: "completed", SpeakerCount: 1, CreatedAt: started, UpdatedAt: started,
	}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	audioDir := filepath.Join(cfg.App.DataDir, "meetings", started.Format("2006"), started.Format("01"), meeting.UID, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"microphone.wav", "system.wav"} {
		writer, err := audio.NewWAVWriter(filepath.Join(audioDir, name), 16000, 1)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]byte, 180000*32)
		for index := 0; index < len(pcm); index += 2 {
			binary.LittleEndian.PutUint16(pcm[index:], uint16(int16(1000)))
		}
		if err := writer.WritePCM16(pcm); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	for _, item := range []struct{ kind, name string }{{"mic_wav", "microphone.wav"}, {"system_wav", "system.wav"}} {
		if _, err := s.makeArtifact(meeting.UID, item.kind, filepath.Join(audioDir, item.name), "audio/wav", "generated"); err != nil {
			t.Fatal(err)
		}
	}
	segments := []model.Segment{
		{ID: "before", StartMS: 0, EndMS: 50000, SpeakerID: "SPEAKER_00", Source: "system", Text: "До границы"},
		{ID: "crossing", StartMS: 90000, EndMS: 110000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Пересекает границу"},
		{ID: "after", StartMS: 120000, EndMS: 150000, SpeakerID: "SPEAKER_00", Source: "system", Text: "После границы"},
	}
	if err := st.SaveTranscriptSegments(meeting.UID, segments); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Иван", Source: "system", PersonID: "person-1", NameSource: "manual"}); err != nil {
		t.Fatal(err)
	}
	diarizationPath := filepath.Join(cfg.App.DataDir, "diarization.json")
	diarization := map[string]any{
		"schema_version": 3, "meeting_uid": meeting.UID, "artifact_type": "diarization", "audio_source": "system",
		"embedding_model": "model", "centroids": map[string][]float32{"SPEAKER_00": {1, 0}},
		"turns":      []model.SpeakerTurn{{StartMS: 0, EndMS: 50000, SpeakerID: "SPEAKER_00"}, {StartMS: 90000, EndMS: 110000, SpeakerID: "SPEAKER_00"}, {StartMS: 120000, EndMS: 150000, SpeakerID: "SPEAKER_00"}},
		"candidates": []model.VoiceCandidate{{SpeakerID: "SPEAKER_00", StartMS: 90000, EndMS: 110000, Score: 0.9}},
	}
	body, _ := json.Marshal(diarization)
	if err := os.WriteFile(diarizationPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.makeArtifact(meeting.UID, "diarization_json", diarizationPath, "application/json", "generated"); err != nil {
		t.Fatal(err)
	}
	attachment := filepath.Join(cfg.App.DataDir, "note.txt")
	if err := os.WriteFile(attachment, []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.makeArtifact(meeting.UID, "text", attachment, "text/plain", "imported"); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(cfg.App.DataDir, "summary.md")
	if err := os.WriteFile(summaryPath, []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.makeArtifact(meeting.UID, "summary", summaryPath, "text/markdown", "generated"); err != nil {
		t.Fatal(err)
	}

	reqBody := bytes.NewBufferString(`{"split_ms":100000,"first_title":"Первая","second_title":"Вторая"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/"+meeting.UID+"/split", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("split failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		First  model.Meeting `json:"first"`
		Second model.Meeting `json:"second"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.First.DurationMS != 100000 || response.Second.DurationMS != 80000 {
		t.Fatalf("unexpected durations: %+v %+v", response.First, response.Second)
	}
	if _, err := st.Meeting(meeting.UID); err == nil {
		t.Fatal("original meeting must be removed")
	}
	firstSegments, err := st.TranscriptSegments(response.First.UID)
	if err != nil {
		t.Fatal(err)
	}
	secondSegments, err := st.TranscriptSegments(response.Second.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstSegments) != 2 || firstSegments[1].EndMS != 100000 || firstSegments[1].Text != "Пересекает границу" {
		t.Fatalf("unexpected first segments: %+v", firstSegments)
	}
	if len(secondSegments) != 2 || secondSegments[0].StartMS != 0 || secondSegments[0].EndMS != 10000 || secondSegments[0].Text != "Пересекает границу" {
		t.Fatalf("unexpected second segments: %+v", secondSegments)
	}
	for _, part := range []model.Meeting{response.First, response.Second} {
		stored, err := st.Meeting(part.UID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Transcript == "" || stored.SpeakerCount != 1 {
			t.Fatalf("split meeting lost transcript or speaker count: %+v", stored)
		}
		speakers, err := st.Speakers(part.UID)
		if err != nil || len(speakers) != 1 || speakers[0].PersonID != "person-1" || speakers[0].NameSource != "manual" {
			t.Fatalf("speaker link was lost for %s: %+v %v", part.UID, speakers, err)
		}
		path := filepath.Join(s.meetingDir(part), "audio", "microphone.wav")
		if duration, err := audio.PCM16WAVDurationMS(path); err != nil || duration != part.DurationMS {
			t.Fatalf("audio duration for %s = %d, want %d (%v)", part.UID, duration, part.DurationMS, err)
		}
		artifacts, _ := st.Artifacts(part.UID)
		var hasDiarization, hasAttachment bool
		for _, artifact := range artifacts {
			if artifact.Type == "diarization_json" {
				hasDiarization = true
			}
			if artifact.Type == "source_text" {
				hasAttachment = true
			}
			if artifact.Type == "summary" {
				t.Fatalf("summary artifact must not be copied to %s", part.UID)
			}
		}
		if !hasDiarization || !hasAttachment {
			t.Fatalf("missing split artifacts for %s: %+v", part.UID, artifacts)
		}
		if part.Summary != "" || part.SummaryStatus == "completed" {
			t.Fatalf("summary must be reset for %s: %+v", part.UID, part)
		}
	}
}

func TestSplitMeetingFallsBackToMixedForMissingSystem(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Now().Add(-3 * time.Minute)
	finished := started.Add(3 * time.Minute)
	meeting := model.Meeting{UID: "meeting-mixed-split", Title: "Mixed", StartedAt: started, FinishedAt: &finished, DurationMS: 180000, Status: "completed", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	audioDir := filepath.Join(cfg.App.DataDir, "meetings", started.Format("2006"), started.Format("01"), meeting.UID, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, amplitude int16) string {
		path := filepath.Join(audioDir, name)
		writer, err := audio.NewWAVWriter(path, 16000, 1)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]byte, 180000*32)
		for index := 0; index < len(pcm); index += 2 {
			binary.LittleEndian.PutUint16(pcm[index:], uint16(amplitude))
		}
		if err := writer.WritePCM16(pcm); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	micPath := write("microphone.wav", 1000)
	mixedPath := write("mixed.wav", 3000)
	s := New(cfg, st, log.New(io.Discard, "", 0))
	if _, err := s.makeArtifact(meeting.UID, "mic_wav", micPath, "audio/wav", "generated"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.makeArtifact(meeting.UID, "mixed_wav", mixedPath, "audio/wav", "generated"); err != nil {
		t.Fatal(err)
	}
	reqBody := bytes.NewBufferString(`{"split_ms":100000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/"+meeting.UID+"/split", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("split failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		First  model.Meeting `json:"first"`
		Second model.Meeting `json:"second"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, part := range []model.Meeting{response.First, response.Second} {
		for _, name := range []string{"microphone.wav", "system.wav", "mixed.wav"} {
			path := filepath.Join(s.meetingDir(part), "audio", name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing %s for %s: %v", name, part.UID, err)
			}
			nonZero := false
			for _, value := range data[44:] {
				if value != 0 {
					nonZero = true
					break
				}
			}
			if !nonZero {
				t.Fatalf("%s is silent for %s; mixed fallback was not used", name, part.UID)
			}
		}
	}
}
