package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeImportedAudioResamplesWAV(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source-48k-stereo.wav")
	target := filepath.Join(dir, "target.wav")
	w, err := NewWAVWriter(source, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 48000*2*2)
	for offset := 0; offset < len(pcm); offset += 4 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(1200)))
		binary.LittleEndian.PutUint16(pcm[offset+2:offset+4], uint16(int16(800)))
	}
	if err = w.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = NormalizeImportedAudio(source, target); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := ReadWAVInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 16000 || info.Channels != 1 || info.BitsPerSample != 16 {
		t.Fatalf("unexpected normalized format: %+v", info)
	}
	if duration, err := PCM16WAVDurationMS(target); err != nil || duration < 990 || duration > 1010 {
		t.Fatalf("unexpected duration=%d err=%v", duration, err)
	}
}

func TestNormalizeImportedAudioDecodesLocalMeetAssistOpus(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "source.wav")
	opusPath := filepath.Join(dir, "source.opus")
	target := filepath.Join(dir, "decoded.wav")
	w, err := NewWAVWriter(wav, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.WritePCM16(make([]byte, 16000*2)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = EncodeWAVToOggOpus(wav, opusPath, "test-meeting", 32); err != nil {
		t.Fatal(err)
	}
	if err = NormalizeImportedAudio(opusPath, target); err != nil {
		t.Fatal(err)
	}
	if duration, err := PCM16WAVDurationMS(target); err != nil || duration < 990 || duration > 1010 {
		t.Fatalf("unexpected duration=%d err=%v", duration, err)
	}
}
