package inference

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// LoadWAV16kMono reads PCM16 WAV and returns normalized mono samples at 16 kHz.
func LoadWAV16kMono(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil || string(riff[:4]) != "RIFF" || string(riff[8:]) != "WAVE" {
		return nil, errors.New("a PCM WAV file was expected")
	}
	var format, channels, bits uint16
	var sampleRate uint32
	var pcm []byte
	for {
		var header [8]byte
		if _, err := io.ReadFull(f, header[:]); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		size := binary.LittleEndian.Uint32(header[4:])
		data := make([]byte, size)
		if _, err := io.ReadFull(f, data); err != nil {
			return nil, err
		}
		if size%2 == 1 {
			_, _ = f.Seek(1, io.SeekCurrent)
		}
		switch string(header[:4]) {
		case "fmt ":
			if len(data) < 16 {
				return nil, errors.New("corrupt WAV fmt chunk")
			}
			format = binary.LittleEndian.Uint16(data[0:])
			channels = binary.LittleEndian.Uint16(data[2:])
			sampleRate = binary.LittleEndian.Uint32(data[4:])
			bits = binary.LittleEndian.Uint16(data[14:])
		case "data":
			pcm = data
		}
	}
	if format != 1 || bits != 16 || channels == 0 || sampleRate == 0 || len(pcm) == 0 {
		return nil, fmt.Errorf("PCM16 WAV is required; format=%d channels=%d rate=%d bits=%d", format, channels, sampleRate, bits)
	}
	frames := len(pcm) / (2 * int(channels))
	mono := make([]float32, frames)
	for i := 0; i < frames; i++ {
		var sum float32
		for c := 0; c < int(channels); c++ {
			off := (i*int(channels) + c) * 2
			sum += float32(int16(binary.LittleEndian.Uint16(pcm[off:]))) / 32768
		}
		mono[i] = sum / float32(channels)
	}
	if sampleRate == 16000 {
		return mono, nil
	}
	outLen := int(math.Round(float64(len(mono)) * 16000 / float64(sampleRate)))
	out := make([]float32, outLen)
	for i := range out {
		pos := float64(i) * float64(sampleRate) / 16000
		left := int(pos)
		if left >= len(mono)-1 {
			out[i] = mono[len(mono)-1]
			continue
		}
		frac := float32(pos - float64(left))
		out[i] = mono[left]*(1-frac) + mono[left+1]*frac
	}
	return out, nil
}
