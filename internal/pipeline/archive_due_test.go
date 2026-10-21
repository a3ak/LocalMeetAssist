package pipeline

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func archiveFixture(t *testing.T, days int, finished time.Time) (*Runner, *store.Store, model.Meeting, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Storage.AudioAfterProcessing = "opus"
	cfg.Storage.AudioArchiveDays = days
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	started := finished.Add(-time.Hour)
	meeting := model.Meeting{UID: "01999999-9999-7999-8999-999999999902", Title: "Archive", StartedAt: started, FinishedAt: &finished, Status: "completed", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	runner := NewWithLogger(cfg, st, log.New(io.Discard, "", 0))
	audioDir := filepath.Join(runner.meetingDir(meeting), "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestWAV(t, filepath.Join(audioDir, "microphone.wav"))
	writeTestWAV(t, filepath.Join(audioDir, "system.wav"))
	writeTestWAV(t, filepath.Join(audioDir, runner.name(meeting, "mixed")+".wav"))
	for _, artifact := range []model.Artifact{
		{UID: "mic", MeetingUID: meeting.UID, Type: "mic_wav", Path: filepath.Join(audioDir, "microphone.wav")},
		{UID: "sys", MeetingUID: meeting.UID, Type: "system_wav", Path: filepath.Join(audioDir, "system.wav")},
		{UID: "mix", MeetingUID: meeting.UID, Type: "mixed_wav", Path: filepath.Join(audioDir, runner.name(meeting, "mixed")+".wav")},
	} {
		artifact.Source = "generated"
		artifact.MIMEType = "audio/wav"
		artifact.CreatedAt = time.Now()
		if err := st.SaveArtifact(artifact); err != nil {
			t.Fatal(err)
		}
	}
	return runner, st, meeting, audioDir
}

func writeTestWAV(t *testing.T, path string) {
	t.Helper()
	writer, err := audio.NewWAVWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WritePCM16(make([]byte, 16000*2)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestArchiveDueConvertsOldMeetings keeps the WAVs until the delay expires and
// then replaces them with one Opus file.
func TestArchiveDueConvertsOldMeetings(t *testing.T) {
	finished := time.Now().Add(-10 * 24 * time.Hour)
	runner, st, meeting, audioDir := archiveFixture(t, 7, finished)
	defer st.Close()

	runner.ArchiveDue(context.Background(), time.Now())

	opus := filepath.Join(audioDir, runner.name(meeting, "mixed")+".opus")
	if info, err := os.Stat(opus); err != nil || info.Size() == 0 {
		t.Fatalf("the archive was not created: %v", err)
	}
	for _, name := range []string{"microphone.wav", "system.wav", runner.name(meeting, "mixed") + ".wav"} {
		if _, err := os.Stat(filepath.Join(audioDir, name)); !os.IsNotExist(err) {
			t.Fatalf("the source WAV %s was kept: %v", name, err)
		}
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["audio_archived_at"] == "" {
		t.Fatalf("the meeting is not marked as archived: %+v", updated.Metadata)
	}
}

// TestArchiveDueKeepsFreshMeetings leaves audio alone until the delay expires.
func TestArchiveDueKeepsFreshMeetings(t *testing.T) {
	runner, st, meeting, audioDir := archiveFixture(t, 7, time.Now().Add(-2*24*time.Hour))
	defer st.Close()

	runner.ArchiveDue(context.Background(), time.Now())

	opus := filepath.Join(audioDir, runner.name(meeting, "mixed")+".opus")
	if _, err := os.Stat(opus); !os.IsNotExist(err) {
		t.Fatalf("a fresh meeting was archived: %v", err)
	}
	for _, name := range []string{"microphone.wav", "system.wav"} {
		if _, err := os.Stat(filepath.Join(audioDir, name)); err != nil {
			t.Fatalf("the source WAV %s was removed too early: %v", name, err)
		}
	}
}

// TestArchiveDueIgnoresImmediatePolicy proves the loop is a no-op when the
// pipeline archives right away.
func TestArchiveDueIgnoresImmediatePolicy(t *testing.T) {
	runner, st, meeting, audioDir := archiveFixture(t, 0, time.Now().Add(-30*24*time.Hour))
	defer st.Close()

	runner.ArchiveDue(context.Background(), time.Now())

	opus := filepath.Join(audioDir, runner.name(meeting, "mixed")+".opus")
	if _, err := os.Stat(opus); !os.IsNotExist(err) {
		t.Fatalf("the archiver ran with an immediate policy: %v", err)
	}
}

// TestArchiveDueRemovesLeftoversWithoutReencoding keeps an existing archive as
// is: only the stray WAV files disappear.
func TestArchiveDueRemovesLeftoversWithoutReencoding(t *testing.T) {
	runner, st, meeting, audioDir := archiveFixture(t, 7, time.Now().Add(-10*24*time.Hour))
	defer st.Close()

	mixedPath := filepath.Join(audioDir, runner.name(meeting, "mixed")+".wav")
	opusPath := filepath.Join(audioDir, runner.name(meeting, "mixed")+".opus")
	if err := audio.EncodeWAVToOggOpus(mixedPath, opusPath, meeting.UID, 32); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveArtifact(model.Artifact{UID: "opus", MeetingUID: meeting.UID, Type: "mixed_opus", Path: opusPath, MIMEType: "audio/ogg", Source: "generated", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(opusPath)
	if err != nil {
		t.Fatal(err)
	}

	runner.ArchiveDue(context.Background(), time.Now())

	after, err := os.Stat(opusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the existing Opus archive was re-encoded")
	}
	for _, name := range []string{"microphone.wav", "system.wav", runner.name(meeting, "mixed") + ".wav"} {
		if _, err := os.Stat(filepath.Join(audioDir, name)); !os.IsNotExist(err) {
			t.Fatalf("the leftover WAV %s was kept: %v", name, err)
		}
	}
}

// TestArchiveDueFindsWAVsWithoutArtifacts covers old meetings whose artifact
// records are gone while the files are still on disk.
func TestArchiveDueFindsWAVsWithoutArtifacts(t *testing.T) {
	runner, st, meeting, audioDir := archiveFixture(t, 7, time.Now().Add(-10*24*time.Hour))
	defer st.Close()

	mixedPath := filepath.Join(audioDir, runner.name(meeting, "mixed")+".wav")
	if _, err := st.DeleteArtifactsByPaths(meeting.UID, filepath.Join(audioDir, "microphone.wav"), filepath.Join(audioDir, "system.wav"), mixedPath); err != nil {
		t.Fatal(err)
	}

	runner.ArchiveDue(context.Background(), time.Now())

	if info, err := os.Stat(filepath.Join(audioDir, runner.name(meeting, "mixed")+".opus")); err != nil || info.Size() == 0 {
		t.Fatalf("the archive was not created: %v", err)
	}
	if _, err := os.Stat(mixedPath); !os.IsNotExist(err) {
		t.Fatalf("the mixed WAV was kept: %v", err)
	}
}
