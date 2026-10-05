package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndMixWAV(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.wav")
	b := filepath.Join(dir, "b.wav")
	out := filepath.Join(dir, "mix.wav")
	for _, item := range []struct {
		path   string
		sample int16
	}{{a, 1000}, {b, 2000}} {
		w, err := NewWAVWriter(item.path, 16000, 1)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 320)
		for i := 0; i < len(buf); i += 2 {
			binary.LittleEndian.PutUint16(buf[i:i+2], uint16(item.sample))
		}
		if err := w.WritePCM16(buf); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := MixPCM16WAV(a, b, out, 1, 1); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := ReadWAVInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 16000 || info.DataSize != 320 {
		t.Fatalf("unexpected info: %+v", info)
	}
	sample := make([]byte, 2)
	if _, err := f.Read(sample); err != nil {
		t.Fatal(err)
	}
	if got := int16(binary.LittleEndian.Uint16(sample)); got != 3000 {
		t.Fatalf("mixed sample=%d", got)
	}
}

func TestMixAllowsEmptySystemStream(t *testing.T) {
	dir := t.TempDir()
	microphonePath := filepath.Join(dir, "microphone.wav")
	systemPath := filepath.Join(dir, "system.wav")
	mixedPath := filepath.Join(dir, "mixed.wav")

	microphone, err := NewWAVWriter(microphonePath, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]byte, 640)
	for i := 0; i < len(samples); i += 2 {
		binary.LittleEndian.PutUint16(samples[i:i+2], uint16(int16(1200)))
	}
	if err := microphone.WritePCM16(samples); err != nil {
		t.Fatal(err)
	}
	if err := microphone.Close(); err != nil {
		t.Fatal(err)
	}

	system, err := NewWAVWriter(systemPath, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}

	if err := MixPCM16WAV(microphonePath, systemPath, mixedPath, 1, 1); err != nil {
		t.Fatalf("empty system stream must be treated as silence: %v", err)
	}
	mixed, err := os.Open(mixedPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ReadWAVInfo(mixed)
	mixed.Close()
	if err != nil {
		t.Fatal(err)
	}
	if info.DataSize != int64(len(samples)) {
		t.Fatalf("unexpected mixed size: got %d want %d", info.DataSize, len(samples))
	}
}

func TestCreateSilentPCM16WAVLike(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.wav")
	silentPath := filepath.Join(dir, "silent.wav")

	writer, err := NewWAVWriter(sourcePath, 16000, 2)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 1280)
	for offset := 0; offset < len(pcm); offset += 2 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(1234)))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	if err := CreateSilentPCM16WAVLike(sourcePath, silentPath); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(silentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := ReadWAVInfo(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 16000 || info.Channels != 2 || info.BitsPerSample != 16 || info.DataSize != int64(len(pcm)) {
		t.Fatalf("unexpected silent WAV format: %+v", info)
	}
	data := make([]byte, info.DataSize)
	if _, err := file.ReadAt(data, info.DataOffset); err != nil {
		t.Fatal(err)
	}
	for index, value := range data {
		if value != 0 {
			t.Fatalf("silent PCM contains non-zero byte at %d: %d", index, value)
		}
	}
}

func TestSlicePCM16WAV(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.wav")
	slicePath := filepath.Join(dir, "slice.wav")
	writer, err := NewWAVWriter(sourcePath, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 4000) // 2 seconds at 1000 Hz, mono PCM16.
	for frame := 0; frame < 2000; frame++ {
		binary.LittleEndian.PutUint16(pcm[frame*2:frame*2+2], uint16(int16(frame)))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if duration, err := PCM16WAVDurationMS(sourcePath); err != nil || duration != 2000 {
		t.Fatalf("duration=%d err=%v", duration, err)
	}
	if err := SlicePCM16WAV(sourcePath, slicePath, 500, 1500); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(slicePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := ReadWAVInfo(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.DataSize != 2000 {
		t.Fatalf("slice bytes=%d want=2000", info.DataSize)
	}
	sample := make([]byte, 2)
	if _, err := file.ReadAt(sample, info.DataOffset); err != nil {
		t.Fatal(err)
	}
	if got := int16(binary.LittleEndian.Uint16(sample)); got != 500 {
		t.Fatalf("first sliced sample=%d want=500", got)
	}
}
