package store

import (
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/model"
)

func TestMeetingAndArtifacts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "meetings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	m := model.Meeting{UID: "u1", Title: "Test", StartedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtifact(model.Artifact{UID: "a1", MeetingUID: "u1", Path: "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Meeting("u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Test" || len(got.ArtifactUIDs) != 1 {
		t.Fatalf("unexpected meeting: %+v", got)
	}
	items, err := s.ListMeetings(nil, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestDeleteArtifactsByPathsIsScopedToMeeting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "meetings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, artifact := range []model.Artifact{
		{UID: "a1", MeetingUID: "m1", Path: "/tmp/one.wav", CreatedAt: now},
		{UID: "a2", MeetingUID: "m1", Path: "/tmp/two.wav", CreatedAt: now},
		{UID: "a3", MeetingUID: "m2", Path: "/tmp/one.wav", CreatedAt: now},
	} {
		if err := s.SaveArtifact(artifact); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := s.DeleteArtifactsByPaths("m1", "/tmp/one.wav")
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0].UID != "a1" {
		t.Fatalf("unexpected deleted artifacts: %+v", deleted)
	}
	if _, err := s.Artifact("a1"); err == nil {
		t.Fatal("a1 must be deleted")
	}
	if _, err := s.Artifact("a2"); err != nil {
		t.Fatalf("a2 must remain: %v", err)
	}
	if _, err := s.Artifact("a3"); err != nil {
		t.Fatalf("same path in another meeting must remain: %v", err)
	}
}
