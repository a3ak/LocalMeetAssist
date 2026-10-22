package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func TestSpeakerEditPreservesVoiceLink(t *testing.T) {
	s, st, meeting := newSpeakerServer(t)
	if err := st.SaveSpeaker(model.Speaker{
		MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Анна", Source: "system",
		PersonID: "person-1", MatchScore: 0.91, NameSource: "auto",
		MatchCandidates: []model.SpeakerMatch{{PersonID: "person-1", Name: "Анна", Score: 0.91}},
	}); err != nil {
		t.Fatal(err)
	}
	recorder := patchSpeakers(t, s, meeting.UID, `{"assignments":{"fragment-2":"SPEAKER_00"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("speaker edit failed: %d %s", recorder.Code, recorder.Body.String())
	}
	speaker, err := st.Speaker(meeting.UID, "SPEAKER_00")
	if err != nil {
		t.Fatal(err)
	}
	if speaker.PersonID != "person-1" || speaker.NameSource != "auto" || speaker.MatchScore == 0 {
		t.Fatalf("voice link was lost during rebuild: %+v", speaker)
	}
}

func TestVoiceEnrollmentSkippedWhenAudioArchived(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Diarization.RememberSpeakers = true
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-archived", Title: "Archived", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "SPEAKER_00", Source: "system"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTranscriptSegments(meeting.UID, []model.Segment{{ID: "fragment-1", StartMS: 0, EndMS: 8000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Реплика"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.App.DataDir, "meetings", now.Format("2006"), now.Format("01"), meeting.UID, "transcript")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "diarization.json")
	body := `{"schema_version":2,"audio_source":"system","turns":[{"start_ms":0,"end_ms":8000,"speaker_id":"SPEAKER_00"}],"candidates":[]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveArtifact(model.Artifact{UID: "diar", MeetingUID: meeting.UID, Type: "diarization_json", Path: path, MIMEType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	recorder := patchSpeakers(t, s, meeting.UID, `{"names":{"SPEAKER_00":"Иван"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("speaker edit failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		VoiceEnrollment struct {
			Skipped  bool     `json:"skipped"`
			Reason   string   `json:"reason"`
			Warnings []string `json:"warnings"`
		} `json:"voice_enrollment"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.VoiceEnrollment.Skipped || response.VoiceEnrollment.Reason != "audio_archived" {
		t.Fatalf("unexpected enrollment result: %s", recorder.Body.String())
	}
	people, err := st.People()
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 0 {
		t.Fatalf("an archived meeting must not create a profile: %+v", people)
	}
}

func TestPeopleListAndDeleteSample(t *testing.T) {
	s, st, _ := newSpeakerServer(t)
	person := model.Person{ID: "person-1", Name: "Иван", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.SavePerson(person); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(s.config().App.DataDir, "voices", person.ID, "sample-1.opus")
	if err := os.MkdirAll(filepath.Dir(clip), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clip, []byte("opus"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVoiceSample(model.VoiceSample{
		ID: "sample-1", PersonID: person.ID, MeetingUID: "meeting-new-speaker",
		Vector: []float32{1, 0}, Dim: 2, ModelSHA256: "hash", ClipPath: clip,
		StartMS: 1000, EndMS: 7000, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/people", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("people list failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var views []model.PersonView
	if err := json.Unmarshal(recorder.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "Иван" || len(views[0].Samples) != 1 {
		t.Fatalf("unexpected people list: %s", recorder.Body.String())
	}
	if views[0].Samples[0].ClipURL == "" {
		t.Fatalf("clip URL is missing: %+v", views[0].Samples[0])
	}
	if strings.Contains(recorder.Body.String(), `"vector"`) {
		t.Fatalf("the embedding vector must not be exposed: %s", recorder.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/people/"+person.ID+"/samples/sample-1", nil)
	del.Header.Set("X-Meeting-Token", s.token)
	deleteRecorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleteRecorder, del)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("sample delete failed: %d %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := os.Stat(clip); !os.IsNotExist(err) {
		t.Fatalf("clip was not removed: %v", err)
	}
	if _, err := st.Person(person.ID); err == nil {
		t.Fatal("a profile without samples must be deleted")
	}
}

func TestAutoNameDoesNotEnrollOnPlainSave(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Diarization.RememberSpeakers = true
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-auto", Title: "Auto", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePerson(model.Person{ID: "person-1", Name: "Иван", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSpeaker(model.Speaker{
		MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Иван", Source: "system",
		PersonID: "person-1", MatchScore: 0.93, NameSource: "auto",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTranscriptSegments(meeting.UID, []model.Segment{{ID: "fragment-1", StartMS: 0, EndMS: 5000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Реплика"}}); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	recorder := patchSpeakers(t, s, meeting.UID, `{"names":{}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("plain save failed: %d %s", recorder.Code, recorder.Body.String())
	}
	samples, err := st.VoiceSamples("person-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("an automatic name must not add samples: %+v", samples)
	}
	if strings.Contains(recorder.Body.String(), "voice_enrollment") {
		t.Fatalf("plain save must not report enrollment: %s", recorder.Body.String())
	}
}
