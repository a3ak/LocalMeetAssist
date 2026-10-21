package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
)

func retentionMeeting(t *testing.T, s *Server, uid string, started time.Time) model.Meeting {
	t.Helper()
	meeting := model.Meeting{UID: uid, Title: "Retention", StartedAt: started, Status: "completed", CreatedAt: started, UpdatedAt: started}
	if err := s.store.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	dir := s.meetingDir(meeting)
	if err := os.MkdirAll(filepath.Join(dir, "audio"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audio", "microphone.wav"), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	return meeting
}

// TestRetentionRemovesExpiredMeetings deletes only meetings older than the limit
// and removes their directory.
func TestRetentionRemovesExpiredMeetings(t *testing.T) {
	s := newBrowserServer(t, "auto")
	cfg := s.config()
	cfg.Storage.MeetingRetentionDays = 365
	s.setConfig(cfg)

	old := retentionMeeting(t, s, "01999999-9999-7999-8999-999999999911", time.Now().Add(-400*24*time.Hour))
	fresh := retentionMeeting(t, s, "01999999-9999-7999-8999-999999999912", time.Now().Add(-10*24*time.Hour))

	s.removeExpiredMeetings(context.Background(), time.Now())

	if _, err := s.store.Meeting(old.UID); err == nil {
		t.Fatal("the expired meeting is still in the store")
	}
	if _, err := os.Stat(s.meetingDir(old)); !os.IsNotExist(err) {
		t.Fatalf("the expired meeting directory was kept: %v", err)
	}
	if _, err := s.store.Meeting(fresh.UID); err != nil {
		t.Fatalf("a fresh meeting was deleted: %v", err)
	}
	if _, err := os.Stat(s.meetingDir(fresh)); err != nil {
		t.Fatalf("the fresh meeting directory was removed: %v", err)
	}
}

// TestRetentionZeroKeepsEverything treats 0 as "keep forever".
func TestRetentionZeroKeepsEverything(t *testing.T) {
	s := newBrowserServer(t, "auto")
	cfg := s.config()
	cfg.Storage.MeetingRetentionDays = 0
	s.setConfig(cfg)

	old := retentionMeeting(t, s, "01999999-9999-7999-8999-999999999913", time.Now().Add(-4000*24*time.Hour))
	s.removeExpiredMeetings(context.Background(), time.Now())

	if _, err := s.store.Meeting(old.UID); err != nil {
		t.Fatalf("a meeting was deleted with retention disabled: %v", err)
	}
}

// TestRetentionWaitsForRecording proves nothing is deleted while a meeting runs.
func TestRetentionWaitsForRecording(t *testing.T) {
	s := newBrowserServer(t, "auto")
	cfg := s.config()
	cfg.Storage.MeetingRetentionDays = 365
	s.setConfig(cfg)

	old := retentionMeeting(t, s, "01999999-9999-7999-8999-999999999914", time.Now().Add(-400*24*time.Hour))
	meeting, err := s.startMeetingRecording(context.Background(), model.RecordingStart{}, "recorded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.recorder.Stop(meeting.UID) }()

	s.removeExpiredMeetings(context.Background(), time.Now())

	if _, err := s.store.Meeting(old.UID); err != nil {
		t.Fatalf("a meeting was deleted during a recording: %v", err)
	}
}

// TestStorageRetentionDefaults pins the shipped defaults: audio is archived
// three days after a meeting, meetings are kept for a year.
func TestStorageRetentionDefaults(t *testing.T) {
	cfg := config.Defaults()
	if cfg.Storage.AudioArchiveDays != 3 {
		t.Fatalf("audio archive delay = %d, want 3", cfg.Storage.AudioArchiveDays)
	}
	if cfg.Storage.MeetingRetentionDays != 365 {
		t.Fatalf("meeting retention = %d, want 365", cfg.Storage.MeetingRetentionDays)
	}
	fields := map[string]settingField{}
	for _, group := range settingsSchema(cfg, nil) {
		for _, field := range group.Fields {
			fields[field.Key] = field
		}
	}
	if field, ok := fields["storage.audio_archive_days"]; !ok || field.Value != "3" {
		t.Fatalf("archive delay field: %+v", field)
	}
	if field, ok := fields["storage.meeting_retention_days"]; !ok || field.Value != "365" {
		t.Fatalf("retention field: %+v", field)
	}
}
