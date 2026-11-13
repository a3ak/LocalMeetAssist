package server

import (
	"testing"

	"localmeetassist/internal/model"
)

func TestMergedSpeakerLinks(t *testing.T) {
	speakers := []model.Speaker{{ID: "MERGED_01"}, {ID: "MERGED_02"}}
	links := make(map[string]*mergedSpeakerLink)
	mergeSpeakerLink(links, "MERGED_01", model.Speaker{PersonID: "person-1", NameSource: "manual", MatchScore: 0.9, MatchCandidates: []model.SpeakerMatch{{PersonID: "person-1", Score: 0.9}}})
	mergeSpeakerLink(links, "MERGED_01", model.Speaker{PersonID: "person-1", NameSource: "auto", MatchScore: 0.8})
	mergeSpeakerLink(links, "MERGED_02", model.Speaker{PersonID: "person-1", NameSource: "auto"})
	mergeSpeakerLink(links, "MERGED_02", model.Speaker{PersonID: "person-2", NameSource: "auto"})
	applyMergedSpeakerLinks(speakers, links)
	if speakers[0].PersonID != "person-1" || speakers[0].NameSource != "manual" || len(speakers[0].MatchCandidates) != 1 {
		t.Fatalf("confirmed link was not preserved: %+v", speakers[0])
	}
	if speakers[1].PersonID != "" || speakers[1].NameSource != "" {
		t.Fatalf("conflicting link must be cleared: %+v", speakers[1])
	}
}
