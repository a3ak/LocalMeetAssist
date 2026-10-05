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

func TestImportAudioRolesCreateCanonicalPair(t *testing.T) {
	for _, test := range []struct {
		kind         string
		importType   string
		artifactType string
		sourceMode   string
		micSample    int16
		systemSample int16
	}{
		{kind: "audio_microphone", importType: "microphone", artifactType: "mic_wav", sourceMode: "separate", micSample: 1234},
		{kind: "audio_system", importType: "system", artifactType: "system_wav", sourceMode: "separate", systemSample: 1234},
		{kind: "audio_mixed", importType: "mixed", artifactType: "imported_mixed_wav", sourceMode: "mixed", systemSample: 1234},
	} {
		t.Run(test.importType, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.App.DataDir = t.TempDir()
			cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
			st, err := store.Open(cfg.Storage.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
			meeting := model.Meeting{UID: "01999999-eeee-7eee-8eee-999999999999", Title: "Import role", StartedAt: now, Status: "created", CreatedAt: now, UpdatedAt: now}
			if err := st.SaveMeeting(meeting); err != nil {
				t.Fatal(err)
			}

			sourcePath := filepath.Join(t.TempDir(), "source.wav")
			writer, err := audio.NewWAVWriter(sourcePath, 16000, 1)
			if err != nil {
				t.Fatal(err)
			}
			pcm := make([]byte, 640)
			for offset := 0; offset < len(pcm); offset += 2 {
				binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(1234)))
			}
			if err := writer.WritePCM16(pcm); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			s := New(cfg, st, log.New(io.Discard, "", 0))
			artifact, importErr := s.importAudio(meeting, file, test.kind)
			file.Close()
			if importErr != nil {
				t.Fatal(importErr)
			}
			if artifact.Type != test.artifactType {
				t.Fatalf("unexpected artifact type: %s", artifact.Type)
			}
			stored, err := st.Meeting(meeting.UID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Metadata["audio_import_type"] != test.importType || stored.Metadata["audio_import_sources"] != test.importType || stored.Metadata["transcription_source_mode"] != test.sourceMode {
				t.Fatalf("unexpected metadata: %+v", stored.Metadata)
			}
			audioDir := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "audio")
			if got := firstPCM16Sample(t, filepath.Join(audioDir, "microphone.wav")); got != test.micSample {
				t.Fatalf("microphone sample=%d want=%d", got, test.micSample)
			}
			if got := firstPCM16Sample(t, filepath.Join(audioDir, "system.wav")); got != test.systemSample {
				t.Fatalf("system sample=%d want=%d", got, test.systemSample)
			}
		})
	}
}

func TestSequentialMicrophoneAndSystemImportsPreserveBoth(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	meeting := model.Meeting{UID: "01999999-ffff-7fff-8fff-999999999999", Title: "Separate imports", StartedAt: now, Status: "created", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))

	for _, item := range []struct {
		kind   string
		sample int16
	}{
		{kind: "audio_microphone", sample: 1234},
		{kind: "audio_system", sample: 2345},
	} {
		path := filepath.Join(t.TempDir(), item.kind+".wav")
		writer, createErr := audio.NewWAVWriter(path, 16000, 1)
		if createErr != nil {
			t.Fatal(createErr)
		}
		pcm := make([]byte, 640)
		for offset := 0; offset < len(pcm); offset += 2 {
			binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(item.sample))
		}
		if createErr = writer.WritePCM16(pcm); createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = writer.Close(); createErr != nil {
			t.Fatal(createErr)
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		meeting, err = st.Meeting(meeting.UID)
		if err != nil {
			t.Fatal(err)
		}
		_, importErr := s.importAudio(meeting, file, item.kind)
		file.Close()
		if importErr != nil {
			t.Fatal(importErr)
		}
	}

	stored, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata["audio_import_sources"] != "microphone,system" || stored.Metadata["audio_import_type"] != "separate" || stored.Metadata["transcription_source_mode"] != "auto" {
		t.Fatalf("unexpected accumulated import metadata: %+v", stored.Metadata)
	}
	audioDir := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "audio")
	if got := firstPCM16Sample(t, filepath.Join(audioDir, "microphone.wav")); got != 1234 {
		t.Fatalf("microphone was overwritten: %d", got)
	}
	if got := firstPCM16Sample(t, filepath.Join(audioDir, "system.wav")); got != 2345 {
		t.Fatalf("system source is invalid: %d", got)
	}
	artifacts, err := st.Artifacts(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, artifact := range artifacts {
		types[artifact.Type] = true
	}
	if !types["mic_wav"] || !types["system_wav"] {
		t.Fatalf("both source artifacts must be visible: %+v", artifacts)
	}
}

func firstPCM16Sample(t *testing.T, path string) int16 {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := audio.ReadWAVInfo(file)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2)
	if _, err := file.ReadAt(data, info.DataOffset); err != nil {
		t.Fatal(err)
	}
	return int16(binary.LittleEndian.Uint16(data))
}
