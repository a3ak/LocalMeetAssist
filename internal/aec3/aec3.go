// Package aec3 prepares a clean microphone stream with WebRTC AEC3 while
// keeping the original recording untouched.
package aec3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"localmeetassist/internal/aec3/native"
	"localmeetassist/internal/audio"
)

// Result reports whether echo cancellation was applied and how strong the
// measured correlation was.
type Result struct {
	Applied     bool
	Correlation float64
}

// CleanWAV cancels the system/render signal from microphone/capture. mode is
// off, auto or on. In auto mode a conservative correlation detector avoids
// modifying headset recordings that contain no measurable acoustic echo.
func CleanWAV(microphonePath, systemPath, outputPath, mode string, delayMS int) (Result, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	if mode == "off" {
		return Result{}, nil
	}
	corr, err := echoCorrelation(microphonePath, systemPath, delayMS)
	if err != nil {
		return Result{}, err
	}
	result := Result{Correlation: corr}
	if mode == "auto" && corr < 0.08 {
		return result, nil
	}

	mic, micInfo, err := openPCM16Mono(microphonePath)
	if err != nil {
		return result, err
	}
	defer mic.Close()
	system, sysInfo, err := openPCM16Mono(systemPath)
	if err != nil {
		return result, err
	}
	defer system.Close()
	if micInfo.SampleRate != sysInfo.SampleRate {
		return result, errors.New("AEC3 requires equal microphone and system sample rates")
	}
	canceller, err := native.New(micInfo.SampleRate, delayMS)
	if err != nil {
		return result, fmt.Errorf("initialize WebRTC AEC3: %w", err)
	}
	defer canceller.Close()
	writer, err := audio.NewWAVWriter(outputPath, micInfo.SampleRate, 1)
	if err != nil {
		return result, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()

	frame := canceller.FrameSamples()
	render := make([]int16, frame)
	capture := make([]int16, frame)
	clean := make([]int16, frame)
	encoded := make([]byte, frame*2)
	for {
		nMic, micErr := readPCM16Frame(mic, capture)
		nSys, sysErr := readPCM16Frame(system, render)
		if nMic == 0 && (micErr == io.EOF || micErr == io.ErrUnexpectedEOF) {
			break
		}
		for i := nMic; i < frame; i++ {
			capture[i] = 0
		}
		for i := nSys; i < frame; i++ {
			render[i] = 0
		}
		if err := canceller.Process(render, capture, clean); err != nil {
			return result, fmt.Errorf("WebRTC AEC3 frame: %w", err)
		}
		for i := 0; i < nMic; i++ {
			binary.LittleEndian.PutUint16(encoded[i*2:i*2+2], uint16(clean[i]))
		}
		if err := writer.WritePCM16(encoded[:nMic*2]); err != nil {
			return result, err
		}
		if micErr != nil && micErr != io.EOF && micErr != io.ErrUnexpectedEOF {
			return result, micErr
		}
		if sysErr != nil && sysErr != io.EOF && sysErr != io.ErrUnexpectedEOF {
			return result, sysErr
		}
	}
	if err := writer.Close(); err != nil {
		return result, err
	}
	closed = true
	result.Applied = true
	return result, nil
}

func openPCM16Mono(path string) (*os.File, audio.WAVInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, audio.WAVInfo{}, err
	}
	info, err := audio.ReadWAVInfo(f)
	if err != nil {
		_ = f.Close()
		return nil, audio.WAVInfo{}, err
	}
	if info.BitsPerSample != 16 || info.Channels != 1 || (info.SampleRate != 16000 && info.SampleRate != 32000 && info.SampleRate != 48000) {
		_ = f.Close()
		return nil, audio.WAVInfo{}, errors.New("WebRTC AEC3 requires mono PCM16 at 16, 32 or 48 kHz")
	}
	if _, err := f.Seek(info.DataOffset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, audio.WAVInfo{}, err
	}
	return f, info, nil
}

func readPCM16Frame(reader io.Reader, samples []int16) (int, error) {
	buf := make([]byte, len(samples)*2)
	n, err := io.ReadFull(reader, buf)
	n -= n % 2
	for i := 0; i < n/2; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2 : i*2+2]))
	}
	return n / 2, err
}

func echoCorrelation(microphonePath, systemPath string, expectedDelayMS int) (float64, error) {
	mic, micInfo, err := openPCM16Mono(microphonePath)
	if err != nil {
		return 0, err
	}
	defer mic.Close()
	sys, sysInfo, err := openPCM16Mono(systemPath)
	if err != nil {
		return 0, err
	}
	defer sys.Close()
	if micInfo.SampleRate != sysInfo.SampleRate {
		return 0, errors.New("echo detector requires equal sample rates")
	}
	limit := micInfo.SampleRate * 30
	micSamples, err := readSamples(mic, limit)
	if err != nil {
		return 0, err
	}
	sysSamples, err := readSamples(sys, limit)
	if err != nil {
		return 0, err
	}
	maxDelayMS := 500
	startMS := 0
	if expectedDelayMS > 0 {
		startMS = expectedDelayMS - 120
		if startMS < 0 {
			startMS = 0
		}
		maxDelayMS = expectedDelayMS + 120
	}
	best := 0.0
	for delayMS := startMS; delayMS <= maxDelayMS; delayMS += 10 {
		delay := delayMS * micInfo.SampleRate / 1000
		if value := normalizedCorrelation(micSamples, sysSamples, delay); value > best {
			best = value
		}
	}
	return best, nil
}

func readSamples(reader io.Reader, limit int) ([]int16, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit*2)))
	if err != nil {
		return nil, err
	}
	data = data[:len(data)-len(data)%2]
	result := make([]int16, len(data)/2)
	for i := range result {
		result[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
	}
	return result, nil
}

func normalizedCorrelation(capture, render []int16, delay int) float64 {
	count := len(render)
	if available := len(capture) - delay; available < count {
		count = available
	}
	if count < 1600 {
		return 0
	}
	var cross, capturePower, renderPower float64
	// Downsampling the detector to 2 kHz keeps startup cost bounded while the
	// actual AEC3 processor still receives every sample.
	step := 8
	for i := 0; i < count; i += step {
		left := float64(capture[i+delay])
		right := float64(render[i])
		cross += left * right
		capturePower += left * left
		renderPower += right * right
	}
	if capturePower == 0 || renderPower == 0 {
		return 0
	}
	return math.Abs(cross / math.Sqrt(capturePower*renderPower))
}
