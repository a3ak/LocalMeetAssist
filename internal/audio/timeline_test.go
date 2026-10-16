package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestMergePCM16WAVTimelineKeepsGapAndSkipsOverlap(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, value int16, frames int) string {
		path := filepath.Join(dir, name)
		writer, err := NewWAVWriter(path, 1000, 1)
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, frames*2)
		for index := 0; index < frames; index++ {
			binary.LittleEndian.PutUint16(data[index*2:], uint16(value))
		}
		if err := writer.WritePCM16(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := write("first.wav", 100, 1000)
	second := write("second.wav", 200, 1000)
	third := write("third.wav", 300, 1000)
	output := filepath.Join(dir, "timeline.wav")
	if err := MergePCM16WAVTimeline([]TimelineClip{{Path: first}, {Path: second, StartMS: 1500}, {Path: third, StartMS: 2200}}, output); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := ReadWAVInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.DataSize / 2; got != 3200 {
		t.Fatalf("frames=%d want=3200", got)
	}
	checks := map[int64]int16{0: 100, 1100: 0, 1600: 200, 2300: 200, 2600: 300}
	for frame, want := range checks {
		var b [2]byte
		if _, err := f.ReadAt(b[:], info.DataOffset+frame*2); err != nil {
			t.Fatal(err)
		}
		if got := int16(binary.LittleEndian.Uint16(b[:])); got != want {
			t.Fatalf("frame %d=%d want=%d", frame, got, want)
		}
	}
}
