package pipeline

import (
	"context"
	"fmt"
	"strings"

	"localmeetassist/internal/config"
	"localmeetassist/internal/inference"
	"localmeetassist/internal/model"
	"localmeetassist/internal/whispercpp"
)

// WhisperNative transcribes audio through the whisper.cpp native runtime.
type WhisperNative struct {
	Config        config.Transcription
	RuntimePath   string
	ONNXRuntime   string
	ChunkProgress func(source string, completed, total int)
}

// Transcribe runs whisper.cpp over one audio file.
func (w *WhisperNative) Transcribe(ctx context.Context, audioPath, source string) ([]model.Segment, error) {
	samples, err := inference.LoadWAV16kMono(audioPath)
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	regions := []speechRegion{{0, len(samples)}}
	if w.Config.VADEnabled {
		if err := inference.EnsureRuntime(w.ONNXRuntime); err != nil {
			return nil, err
		}
		regions, err = sileroRegions(ctx, samples, w.Config)
		if err != nil {
			return nil, fmt.Errorf("Silero VAD: %w", err)
		}
	}
	chunkSeconds := w.Config.ChunkSeconds
	if chunkSeconds <= 0 || chunkSeconds > 30 {
		chunkSeconds = 30
	}
	regions = splitSpeechRegions(regions, chunkSeconds*16000)
	if len(regions) == 0 {
		return nil, nil
	}
	runtime, err := whispercpp.Open(w.RuntimePath, w.Config.ModelPath, w.Config.Language, w.Config.Threads)
	if err != nil {
		return nil, fmt.Errorf("Whisper.cpp: %w", err)
	}
	defer runtime.Close()
	result := make([]model.Segment, 0, len(regions)*2)
	for index, region := range regions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk := samples[region.start:region.end]
		actualSamples := len(chunk)
		if len(chunk) < 16000 {
			padded := make([]float32, 16000)
			copy(padded, chunk)
			chunk = padded
		}
		segments, transcribeErr := runtime.Transcribe(chunk)
		if transcribeErr != nil {
			return nil, fmt.Errorf("Whisper chunk %d/%d: %w", index+1, len(regions), transcribeErr)
		}
		regionDuration := int64(actualSamples) * 1000 / 16000
		regionStart := int64(region.start) * 1000 / 16000
		for _, segment := range segments {
			text := strings.TrimSpace(segment.Text)
			if text == "" || segment.StartMS >= regionDuration {
				continue
			}
			end := segment.EndMS
			if end > regionDuration {
				end = regionDuration
			}
			result = append(result, model.Segment{StartMS: regionStart + segment.StartMS, EndMS: regionStart + end, Source: source, Text: text})
		}
		if w.ChunkProgress != nil {
			w.ChunkProgress(source, index+1, len(regions))
		}
	}
	return result, nil
}
