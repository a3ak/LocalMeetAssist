package audio

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEncodeWAVToOggOpus(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "meeting.wav")
	opusPath := filepath.Join(dir, "meeting.opus")
	writer, err := NewWAVWriter(wavPath, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 16000*2)
	for i := 0; i < 16000; i++ {
		// A deterministic voice-like square wave is sufficient to exercise the
		// encoder and Ogg muxer without a binary fixture.
		value := int16(4000)
		if (i/80)%2 == 1 {
			value = -value
		}
		binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(value))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := EncodeWAVToOggOpus(wavPath, opusPath, "meeting-test", 32); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(opusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("OggS")) || !bytes.Contains(data, []byte("OpusHead")) || !bytes.Contains(data, []byte("OpusTags")) {
		t.Fatalf("invalid Ogg/Opus headers: %x", data[:min(len(data), 64)])
	}
	// ffprobe is only an independent test oracle when present; the application
	// never calls it and remains free of FFmpeg at runtime.
	if ffprobe, lookErr := exec.LookPath("ffprobe"); lookErr == nil {
		cmd := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,sample_rate,channels", "-of", "default=nw=1", opusPath)
		probe, probeErr := cmd.CombinedOutput()
		if probeErr != nil || !bytes.Contains(probe, []byte("codec_name=opus")) {
			t.Fatalf("ffprobe rejected generated Opus: %v: %s", probeErr, probe)
		}
	}
}
