package pipeline

import (
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func cleanTestWindow(frames int, otherProbability float32) (segmentationWindow, []int) {
	window := segmentationWindow{startSample: 0, frames: frames, probability: make([]float32, frames*3)}
	for frame := 0; frame < frames; frame++ {
		window.probability[frame*3] = 0.90
		window.probability[frame*3+1] = otherProbability
	}
	labels := make([]int, frames*3)
	for index := range labels {
		labels[index] = -1
	}
	for frame := 0; frame < frames; frame++ {
		labels[frame*3] = 0
	}
	return window, labels
}

func TestCleanVoiceCandidatesKeepsDominantRun(t *testing.T) {
	window, labels := cleanTestWindow(100, 0.10)
	candidates := cleanVoiceCandidates([]segmentationWindow{window}, labels, 160000)
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %+v", candidates)
	}
	candidate := candidates[0]
	if candidate.SpeakerID != "SPEAKER_00" || candidate.StartMS != 0 || candidate.EndMS != 1687 {
		t.Fatalf("unexpected candidate: %+v", candidate)
	}
	if candidate.Score <= 0.2 {
		t.Fatalf("unexpected score: %v", candidate.Score)
	}
}

func TestCleanVoiceCandidatesRejectsOverlap(t *testing.T) {
	window, labels := cleanTestWindow(100, 0.55)
	candidates := cleanVoiceCandidates([]segmentationWindow{window}, labels, 160000)
	if len(candidates) != 0 {
		t.Fatalf("overlapping frames must not produce a candidate: %+v", candidates)
	}
}

func TestSpeakerCentroidsNormalizesAndSkipsInvalid(t *testing.T) {
	observations := []speakerObservation{
		{embedding: []float32{1, 0}, valid: true},
		{embedding: []float32{0, 1}, valid: true},
		{embedding: []float32{0, 1}, valid: false},
	}
	centroids := speakerCentroids(observations, []int{0, 1, 1})
	if len(centroids) != 2 {
		t.Fatalf("expected two centroids, got %+v", centroids)
	}
	if centroids["SPEAKER_00"][0] != 1 || centroids["SPEAKER_00"][1] != 0 {
		t.Fatalf("unexpected SPEAKER_00 centroid: %v", centroids["SPEAKER_00"])
	}
	if centroids["SPEAKER_01"][0] != 0 || centroids["SPEAKER_01"][1] != 1 {
		t.Fatalf("unexpected SPEAKER_01 centroid: %v", centroids["SPEAKER_01"])
	}
}

func TestSaveSpeakersAppliesAutoNameAndKeepsManual(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-voice", Title: "Voice", StartedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	segments := []model.Segment{{ID: "fragment-1", StartMS: 0, EndMS: 5000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Реплика"}}
	runner := New(config.Defaults(), st)
	if _, err := runner.saveSpeakers(meeting.UID, segments, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	matches := map[string]speakerMatch{
		"SPEAKER_00": {PersonID: "person-1", Name: "Иван", Score: 0.93, Auto: true},
	}
	if err := runner.applySpeakerMatches(&loaded, matches); err != nil {
		t.Fatal(err)
	}
	speaker, err := st.Speaker(meeting.UID, "SPEAKER_00")
	if err != nil {
		t.Fatal(err)
	}
	if speaker.DisplayName != "Иван" || speaker.PersonID != "person-1" || speaker.NameSource != "auto" {
		t.Fatalf("automatic name was not applied: %+v", speaker)
	}
	// A manual name must survive a later automatic match.
	speaker.NameSource = "manual"
	if err := st.SaveSpeaker(speaker); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	other := map[string]speakerMatch{
		"SPEAKER_00": {PersonID: "person-2", Name: "Пётр", Score: 0.99, Auto: true},
	}
	if err := runner.applySpeakerMatches(&loaded, other); err != nil {
		t.Fatal(err)
	}
	speaker, err = st.Speaker(meeting.UID, "SPEAKER_00")
	if err != nil {
		t.Fatal(err)
	}
	if speaker.DisplayName != "Иван" || speaker.PersonID != "person-1" {
		t.Fatalf("a manual name was overwritten: %+v", speaker)
	}
}
