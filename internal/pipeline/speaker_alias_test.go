package pipeline

import (
	"testing"

	"localmeetassist/internal/model"
)

func TestSpeakerAliasesByOverlap(t *testing.T) {
	existing := []model.Speaker{
		{ID: "SPEAKER_00", Fragments: []model.Sample{{StartMS: 0, EndMS: 50000, Source: "system"}}},
		{ID: "SPEAKER_01", Fragments: []model.Sample{{StartMS: 60000, EndMS: 100000, Source: "system"}}},
	}
	segments := []model.Segment{
		{SpeakerID: "SPEAKER_05", StartMS: 0, EndMS: 40000, Source: "system"},
		{SpeakerID: "SPEAKER_06", StartMS: 70000, EndMS: 100000, Source: "system"},
		{SpeakerID: "SPEAKER_07", StartMS: 110000, EndMS: 120000, Source: "system"},
	}
	aliases := speakerAliasesByOverlap(existing, segments)
	if aliases["SPEAKER_05"] != "SPEAKER_00" || aliases["SPEAKER_06"] != "SPEAKER_01" {
		t.Fatalf("unexpected aliases: %+v", aliases)
	}
	if _, ok := aliases["SPEAKER_07"]; ok {
		t.Fatalf("a cluster without overlap must not inherit a name: %+v", aliases)
	}
}

func TestSpeakerAliasesDoNotReuseOneOldSpeaker(t *testing.T) {
	existing := []model.Speaker{{ID: "SPEAKER_00", Fragments: []model.Sample{{StartMS: 0, EndMS: 60000, Source: "system"}}}}
	segments := []model.Segment{
		{SpeakerID: "SPEAKER_05", StartMS: 0, EndMS: 30000, Source: "system"},
		{SpeakerID: "SPEAKER_06", StartMS: 30000, EndMS: 60000, Source: "system"},
	}
	aliases := speakerAliasesByOverlap(existing, segments)
	count := 0
	for _, oldID := range aliases {
		if oldID == "SPEAKER_00" {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("one old speaker was inherited twice: %+v", aliases)
	}
}
