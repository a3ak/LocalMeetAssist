package pipeline

import "testing"

func TestExactSpeakerCountClustering(t *testing.T) {
	observations := []speakerObservation{
		{window: 0, embedding: []float32{1, 0}, valid: true},
		{window: 0, embedding: []float32{0, 1}, valid: true},
		{window: 1, embedding: []float32{.99, .01}, valid: true},
		{window: 1, embedding: []float32{.01, .99}, valid: true},
	}
	labels := clusterSpeakerEmbeddings(observations, 2, .6)
	if labels[0] != labels[2] || labels[1] != labels[3] || labels[0] == labels[1] {
		t.Fatalf("unexpected labels: %v", labels)
	}
}

func TestSplitSpeechRegionsKeepsTail(t *testing.T) {
	got := splitSpeechRegions([]speechRegion{{start: 100, end: 35100}}, 16000)
	if len(got) != 3 || got[2].start != 32100 || got[2].end != 35100 {
		t.Fatalf("unexpected regions: %#v", got)
	}
}
