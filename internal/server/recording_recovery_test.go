package server

import (
	"encoding/binary"
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

func writeInterruptedWAV(t *testing.T, path string) {
	t.Helper()
	writer, err := audio.NewWAVWriter(path, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 32000*2)
	for index := 0; index < len(pcm); index += 2 {
		binary.LittleEndian.PutUint16(pcm[index:], uint16(int16(1000)))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt([]byte{0, 0, 0, 0}, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0, 0, 0, 0}, 40); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverInterruptedRecordings(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Now().Add(-2 * time.Minute)
	meeting := model.Meeting{UID: "meeting-interrupted", Title: "Interrupted", StartedAt: started, Status: "recording", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.App.DataDir, "meetings", started.Format("2006"), started.Format("01"), meeting.UID, "audio")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	micPath := filepath.Join(dir, "microphone.wav")
	systemPath := filepath.Join(dir, "system.wav")
	writeInterruptedWAV(t, micPath)
	writeInterruptedWAV(t, systemPath)
	s := New(cfg, st, log.New(io.Discard, "", 0))
	if recovered := s.RecoverInterruptedRecordings(); recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "failed" || updated.DurationMS != 2000 {
		t.Fatalf("unexpected meeting state: %+v", updated)
	}
	if updated.LastError == "" || updated.FinishedAt == nil {
		t.Fatalf("interrupted recording was not explained: %+v", updated)
	}
	file, err := os.Open(micPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := audio.ReadWAVInfo(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(micPath)
	if info.DataSize != stat.Size()-info.DataOffset {
		t.Fatalf("WAV was not repaired: %+v", info)
	}
}
