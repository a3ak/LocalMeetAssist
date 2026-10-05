package pipeline

import (
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

func TestEnsureCanonicalAudioRestoresOpusOnlyMeeting(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	meeting := model.Meeting{UID: "01999999-9999-7999-8999-999999999901", Title: "Opus", StartedAt: started, Status: "created", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	audioDir := filepath.Join(dir, "meetings", "2026", "09", meeting.UID, "audio")
	sourceWAV := filepath.Join(audioDir, "source.wav")
	opusPath := filepath.Join(audioDir, "archived.opus")
	writer, err := audio.NewWAVWriter(sourceWAV, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WritePCM16(make([]byte, 32000)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := audio.EncodeWAVToOggOpus(sourceWAV, opusPath, meeting.UID, 32); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourceWAV); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveArtifact(model.Artifact{UID: "opus-artifact", MeetingUID: meeting.UID, Type: "mixed_opus", Path: opusPath, MIMEType: "audio/ogg", Source: "recovered", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	runner := NewWithLogger(cfg, st, log.New(io.Discard, "", 0))
	micPath := filepath.Join(audioDir, "microphone.wav")
	systemPath := filepath.Join(audioDir, "system.wav")
	restored, err := runner.ensureCanonicalAudio(&meeting, micPath, systemPath)
	if err != nil {
		t.Fatal(err)
	}
	if !restored || !validCanonicalWAV(micPath) || !validCanonicalWAV(systemPath) {
		t.Fatal("canonical WAV pair was not restored")
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["audio_import_sources"] != "mixed" || updated.Metadata["transcription_source_mode"] != "mixed" {
		t.Fatalf("unexpected restored metadata: %+v", updated.Metadata)
	}
	if _, err := os.Stat(opusPath); err != nil {
		t.Fatalf("source Opus was removed: %v", err)
	}
}
