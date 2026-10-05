package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/store"
)

func TestReconcileMeetingFilesRestoresCalendarArtifactsAndSpeakers(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.App.MicrophoneOwnerName = "Заур"
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	uid := "01999999-9999-7999-8999-999999999900"
	meetingDir := filepath.Join(dir, "meetings", "2026", "09", uid)
	if err := os.MkdirAll(filepath.Join(meetingDir, "audio"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 24, 15, 30, 0, 0, time.UTC)
	metadata := fmt.Sprintf("{\n  \"meeting_uid\": %q,\n  \"title\": \"Старая встреча\",\n  \"started_at\": %q,\n  \"status\": \"completed\"\n}\n", uid, started.Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(meetingDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := "---\nmeeting_uid: " + uid + "\nartifact_type: transcript\n---\n\n[00:00] Заур: Начинаем.\n[00:05] Антон: Добрый день.\n"
	if err := os.MkdirAll(filepath.Join(meetingDir, "transcript"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript", "transcript.md"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(meetingDir, "summary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "summary", "summary.md"), []byte("---\nartifact_type: summary\n---\n\nОбсудили миграцию."), 0o600); err != nil {
		t.Fatal(err)
	}
	writeReconcileTestWAV(t, filepath.Join(meetingDir, "audio", "microphone.wav"))

	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := New(cfg, st, log.New(io.Discard, "", 0))
	report := server.ReconcileMeetingFiles()
	if report.Imported != 1 || len(report.Warnings) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	meeting, err := st.Meeting(uid)
	if err != nil {
		t.Fatal(err)
	}
	if meeting.Title != "Старая встреча" || meeting.Transcript == "" || meeting.Summary == "" || meeting.SpeakerCount != 2 {
		t.Fatalf("meeting was not fully restored: %+v", meeting)
	}
	speakers, err := st.Speakers(uid)
	if err != nil || len(speakers) != 2 {
		t.Fatalf("speakers=%+v err=%v", speakers, err)
	}
	artifacts, err := st.Artifacts(uid)
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
	second := server.ReconcileMeetingFiles()
	if second.Imported != 0 || second.Artifacts != 0 {
		t.Fatalf("startup reconciliation is not idempotent: %+v", second)
	}
}

func writeReconcileTestWAV(t *testing.T, path string) {
	t.Helper()
	writer, err := audio.NewWAVWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 32000)
	for offset := 0; offset < len(pcm); offset += 2 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(120)))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}
