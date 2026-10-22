package voice

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func TestCohesiveSamples(t *testing.T) {
	same := []preparedSample{
		{vector: []float32{1, 0}},
		{vector: []float32{0.98, 0.1}},
	}
	if !cohesiveSamples(same) {
		t.Fatal("similar samples must be cohesive")
	}
	mixed := []preparedSample{
		{vector: []float32{1, 0}},
		{vector: []float32{0, 1}},
	}
	if cohesiveSamples(mixed) {
		t.Fatal("orthogonal samples must not be cohesive")
	}
}

func TestCandidatesFromTurnsSubtractsOverlap(t *testing.T) {
	turns := []model.SpeakerTurn{
		{StartMS: 0, EndMS: 10000, SpeakerID: "SPEAKER_00"},
		{StartMS: 2000, EndMS: 4000, SpeakerID: "SPEAKER_01"},
		{StartMS: 5000, EndMS: 6000, SpeakerID: "SPEAKER_02"},
	}
	candidates := candidatesFromTurns(turns, "SPEAKER_00")
	if len(candidates) != 2 {
		t.Fatalf("expected two clean intervals of at least 1.5 s, got %+v", candidates)
	}
	// The first interval is 0-2 s; 4-5 s is too short. The last one is 6-10 s.
	want := [][2]int64{{6000, 10000}, {0, 2000}}
	for index, candidate := range candidates {
		if candidate.StartMS != want[index][0] || candidate.EndMS != want[index][1] {
			t.Fatalf("candidate %d = %d-%d, want %d-%d", index, candidate.StartMS, candidate.EndMS, want[index][0], want[index][1])
		}
	}
}

func TestSelectCandidatesDropsOverlaps(t *testing.T) {
	candidates := []model.VoiceCandidate{
		{SpeakerID: "SPEAKER_00", StartMS: 0, EndMS: 5000, Score: 0.9},
		{SpeakerID: "SPEAKER_00", StartMS: 1000, EndMS: 6000, Score: 0.8},
		{SpeakerID: "SPEAKER_01", StartMS: 0, EndMS: 5000, Score: 0.7},
	}
	selected := selectCandidates(candidates, nil, "SPEAKER_00")
	if len(selected) != 1 {
		t.Fatalf("expected one non-overlapping candidate, got %+v", selected)
	}
}

func TestVoiceMetricsRejectsSilence(t *testing.T) {
	rms, speech, clipped := voiceMetrics(make([]float32, 16000))
	if rms != 0 || speech != 0 || clipped != 0 {
		t.Fatalf("silence metrics = %v %v %v", rms, speech, clipped)
	}
	if qualityAcceptable(model.VoiceQuality{DurationMS: 2000}) {
		t.Fatal("silent sample must not be acceptable")
	}
}

func TestMatchRequiresMatureProfileAndMargin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	modelPath := filepath.Join(dir, "embedding.onnx")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	defer service.Close()
	modelHash, err := service.ModelHash(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := model.Person{ID: "person-1", Name: "Иван", CreatedAt: now, UpdatedAt: now}
	second := model.Person{ID: "person-2", Name: "Пётр", CreatedAt: now, UpdatedAt: now}
	for _, person := range []model.Person{first, second} {
		if err := st.SavePerson(person); err != nil {
			t.Fatal(err)
		}
	}
	addSamples := func(personID string, vector []float32) {
		for index := 0; index < 8; index++ {
			meeting := "meeting-a"
			if index >= 4 {
				meeting = "meeting-b"
			}
			if err := st.SaveVoiceSample(model.VoiceSample{
				ID:          personID + "-" + string(rune('a'+index)),
				PersonID:    personID,
				MeetingUID:  meeting,
				Vector:      vector,
				Dim:         len(vector),
				ModelSHA256: modelHash,
				CreatedAt:   now,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	addSamples(first.ID, []float32{1, 0})
	// The second profile is very close to the first, so the margin must block
	// an automatic name even though the score is high.
	addSamples(second.ID, normalize([]float32{0.99, 0.02}))

	cfg := config.Defaults()
	cfg.Diarization.EmbeddingModel = modelPath
	cfg.Diarization.RememberSpeakers = true
	cfg.Diarization.VoiceAutoThreshold = 0.80
	cfg.Diarization.VoiceSuggestThreshold = 0.60
	result := service.Match(cfg, map[string][]float32{"SPEAKER_00": {1, 0}})
	match, ok := result["SPEAKER_00"]
	if !ok {
		t.Fatalf("expected a suggestion, got %+v", result)
	}
	if match.Auto {
		t.Fatalf("a close second profile must block auto naming: %+v", match)
	}
	if len(match.Candidates) < 2 {
		t.Fatalf("expected both candidates, got %+v", match.Candidates)
	}
}

func TestMatchOnlyMatureProfile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	modelPath := filepath.Join(dir, "embedding.onnx")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	modelHash, err := service.ModelHash(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	person := model.Person{ID: "person-1", Name: "Иван", CreatedAt: now, UpdatedAt: now}
	if err := st.SavePerson(person); err != nil {
		t.Fatal(err)
	}
	// Four samples from one meeting are enough for a suggestion but not for
	// an automatic name.
	for index := 0; index < 4; index++ {
		if err := st.SaveVoiceSample(model.VoiceSample{
			ID:          "sample-" + string(rune('a'+index)),
			PersonID:    person.ID,
			MeetingUID:  "meeting-a",
			Vector:      []float32{1, 0},
			Dim:         2,
			ModelSHA256: modelHash,
			CreatedAt:   now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults()
	cfg.Diarization.EmbeddingModel = modelPath
	cfg.Diarization.RememberSpeakers = true
	result := service.Match(cfg, map[string][]float32{"SPEAKER_00": {1, 0}})
	match := result["SPEAKER_00"]
	if match.Auto {
		t.Fatalf("an immature profile must not auto-name: %+v", match)
	}
	if len(match.Candidates) == 0 {
		t.Fatalf("expected a suggestion, got %+v", match)
	}
}

func normalize(vector []float32) []float32 {
	var sum float64
	for _, value := range vector {
		sum += float64(value * value)
	}
	norm := float32(math.Sqrt(sum))
	out := make([]float32, len(vector))
	for index, value := range vector {
		out[index] = value / norm
	}
	return out
}
