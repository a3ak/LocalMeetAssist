package server

import (
	"bytes"
	"fmt"
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

// newSpeakerServer builds a meeting whose diarization found one system speaker
// with three fragments - the case where a second participant has to be added by
// hand.
func newSpeakerServer(t *testing.T) (*Server, *store.Store, model.Meeting) {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-new-speaker", Title: "New speaker", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now, Summary: "Старый протокол", SummaryStatus: "completed"}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Анна", Source: "system"}); err != nil {
		t.Fatal(err)
	}
	segments := []model.Segment{
		{ID: "fragment-1", StartMS: 0, EndMS: 4000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Первая реплика"},
		{ID: "fragment-2", StartMS: 5000, EndMS: 9000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Вторая реплика"},
		{ID: "fragment-3", StartMS: 10000, EndMS: 14000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Третья реплика"},
	}
	if err := st.SaveTranscriptSegments(meeting.UID, segments); err != nil {
		t.Fatal(err)
	}
	return New(cfg, st, log.New(io.Discard, "", 0)), st, meeting
}

// patchSpeakers sends one batch speaker edit.
func patchSpeakers(t *testing.T, s *Server, uid, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/meetings/"+uid+"/speakers", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	return recorder
}

// TestBatchSpeakerEditCreatesSpeakerAndMovesFragments covers the case the
// diarization merged two participants into one voice: a speaker is added and
// fragments are moved into it in one request.
func TestBatchSpeakerEditCreatesSpeakerAndMovesFragments(t *testing.T) {
	s, st, meeting := newSpeakerServer(t)
	body := `{"names":{"SPEAKER_00":"Анна","SPEAKER_01":"Борис"},"assignments":{"fragment-2":"SPEAKER_01"},"create":[{"id":"SPEAKER_01","display_name":"Борис","source":"system"}]}`
	recorder := patchSpeakers(t, s, meeting.UID, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create speaker failed: %d %s", recorder.Code, recorder.Body.String())
	}
	speakers, err := st.Speakers(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(speakers) != 2 {
		t.Fatalf("expected two speakers, got %+v", speakers)
	}
	byID := map[string]model.Speaker{}
	for _, speaker := range speakers {
		byID[speaker.ID] = speaker
	}
	if len(byID["SPEAKER_00"].Fragments) != 2 || len(byID["SPEAKER_01"].Fragments) != 1 {
		t.Fatalf("fragments were not split: %+v", byID)
	}
	if byID["SPEAKER_01"].DisplayName != "Борис" || byID["SPEAKER_01"].Source != "system" {
		t.Fatalf("new speaker was not saved as requested: %+v", byID["SPEAKER_01"])
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated.Transcript, "Борис: Вторая реплика") {
		t.Fatalf("transcript was not rebuilt: %q", updated.Transcript)
	}
	if updated.SpeakerCount != 2 {
		t.Fatalf("speaker count = %d", updated.SpeakerCount)
	}
	if updated.Metadata["summary_stale"] != "true" {
		t.Fatal("the summary was not marked stale after a speaker change")
	}
}

// TestBatchSpeakerEditKeepsEmptyCreatedSpeaker checks that a speaker without
// fragments survives the save: the card would otherwise disappear right after
// it was added and the user would have to add it again.
func TestBatchSpeakerEditKeepsEmptyCreatedSpeaker(t *testing.T) {
	s, st, meeting := newSpeakerServer(t)
	body := `{"create":[{"id":"SPEAKER_01","display_name":"Борис","source":"system"}]}`
	recorder := patchSpeakers(t, s, meeting.UID, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create speaker failed: %d %s", recorder.Code, recorder.Body.String())
	}
	speakers, err := st.Speakers(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(speakers) != 2 || speakers[0].ID != "SPEAKER_00" || speakers[1].ID != "SPEAKER_01" {
		t.Fatalf("the new speaker was dropped: %+v", speakers)
	}
}

// TestBatchSpeakerEditRejectsBadCreation keeps the identifier and the stream of
// a new speaker checked: a duplicate or a foreign source would corrupt the
// speaker state that the transcript is rebuilt from.
func TestBatchSpeakerEditRejectsBadCreation(t *testing.T) {
	cases := map[string]string{
		"duplicate id":   `{"create":[{"id":"SPEAKER_00","display_name":"Кто-то","source":"system"}]}`,
		"empty id":       `{"create":[{"id":"  ","display_name":"Кто-то","source":"system"}]}`,
		"bad characters": `{"create":[{"id":"../evil","display_name":"Кто-то","source":"system"}]}`,
		"unknown stream": `{"create":[{"id":"SPEAKER_01","display_name":"Кто-то","source":"mixed"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, meeting := newSpeakerServer(t)
			recorder := patchSpeakers(t, s, meeting.UID, body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestBatchSpeakerEditAssignsToNewSpeakerOnly checks that moving fragments into
// a speaker of another stream is still refused when the speaker is created in
// the same request.
func TestBatchSpeakerEditAssignsToNewSpeakerOnly(t *testing.T) {
	s, _, meeting := newSpeakerServer(t)
	// The meeting has a system stream only, so a microphone speaker cannot be
	// created and fragments cannot be moved into one.
	body := `{"create":[{"id":"SPEAKER_01","display_name":"Борис","source":"microphone"}],"assignments":{"fragment-1":"SPEAKER_01"}}`
	recorder := patchSpeakers(t, s, meeting.UID, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "source") && !strings.Contains(recorder.Body.String(), "stream") {
		t.Fatalf("unexpected error: %s", recorder.Body.String())
	}
}

// TestSpeakerCreationLimit keeps a runaway batch bounded like the other maps.
func TestSpeakerCreationLimit(t *testing.T) {
	s, _, meeting := newSpeakerServer(t)
	items := make([]string, 0, 51)
	for index := 0; index < 51; index++ {
		items = append(items, fmt.Sprintf(`{"id":"SPEAKER_%02d","source":"system"}`, index+1))
	}
	recorder := patchSpeakers(t, s, meeting.UID, `{"create":[`+strings.Join(items, ",")+`]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", recorder.Code, recorder.Body.String())
	}
}
