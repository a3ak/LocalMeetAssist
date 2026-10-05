package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ort "github.com/yalue/onnxruntime_go"

	"localmeetassist/internal/config"
	"localmeetassist/internal/inference"
	"localmeetassist/internal/model"
)

// GigaAMONNX transcribes audio with the GigaAM v3 E2E RNNT ONNX models.
type GigaAMONNX struct {
	Config        config.Transcription
	RuntimePath   string
	ChunkProgress func(source string, completed, total int)
}

type speechRegion struct{ start, end int }

// Transcribe runs VAD-driven chunk recognition over one audio file.
func (g *GigaAMONNX) Transcribe(ctx context.Context, audioPath, source string) ([]model.Segment, error) {
	if err := inference.EnsureRuntime(g.RuntimePath); err != nil {
		return nil, err
	}
	samples, err := inference.LoadWAV16kMono(audioPath)
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	regions := []speechRegion{{0, len(samples)}}
	if g.Config.VADEnabled {
		regions, err = sileroRegions(ctx, samples, g.Config)
		if err != nil {
			return nil, fmt.Errorf("Silero VAD: %w", err)
		}
	}
	// Chunk length is expressed in samples; 16000 is the sample rate the models expect.
	regions = splitSpeechRegions(regions, max(5, g.Config.ChunkSeconds)*16000)
	if len(regions) == 0 {
		return nil, nil
	}
	decoder, err := newGigaDecoder(g.Config.ModelPath)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	segments := make([]model.Segment, 0, len(regions))
	for i, region := range regions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, decodeErr := decoder.Decode(samples[region.start:region.end])
		if decodeErr != nil {
			return nil, fmt.Errorf("GigaAM chunk %d/%d: %w", i+1, len(regions), decodeErr)
		}
		text = strings.TrimSpace(text)
		if text != "" {
			segments = append(segments, model.Segment{StartMS: int64(region.start) * 1000 / 16000, EndMS: int64(region.end) * 1000 / 16000, Source: source, Text: text})
		}
		if g.ChunkProgress != nil {
			g.ChunkProgress(source, i+1, len(regions))
		}
	}
	return segments, nil
}

func splitSpeechRegions(input []speechRegion, maxSamples int) []speechRegion {
	var result []speechRegion
	for _, region := range input {
		for start := region.start; start < region.end; start += maxSamples {
			end := min(start+maxSamples, region.end)
			// Drop fragments shorter than 100 ms (1600 samples at 16 kHz).
			if end-start >= 1600 {
				result = append(result, speechRegion{start, end})
			}
		}
	}
	return result
}

func sileroRegions(ctx context.Context, samples []float32, cfg config.Transcription) ([]speechRegion, error) {
	session, err := ort.NewDynamicAdvancedSession(cfg.VADModelPath, []string{"input", "state", "sr"}, []string{"output", "stateN"}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Destroy()
	// Silero VAD consumes 512-sample hops with a 64-sample left context and a
	// 2x128 recurrent state, matching the model input signature.
	const hop, contextSize = 512, 64
	state := make([]float32, 2*128)
	var raw []speechRegion
	active, start := false, 0
	// Leaving speech uses a slightly lower threshold than entering it, so a short
	// dip inside one phrase does not split it into separate regions.
	negative := float32(cfg.VADThreshold - .15)
	for frameStart := 0; frameStart < len(samples); frameStart += hop {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame := make([]float32, contextSize+hop)
		contextStart := max(0, frameStart-contextSize)
		copy(frame[contextSize-(frameStart-contextStart):contextSize], samples[contextStart:frameStart])
		copy(frame[contextSize:], samples[frameStart:min(frameStart+hop, len(samples))])
		input, _ := ort.NewTensor(ort.Shape{1, contextSize + hop}, frame)
		stateInput, _ := ort.NewTensor(ort.Shape{2, 1, 128}, state)
		rate, _ := ort.NewTensor(ort.Shape{1}, []int64{16000})
		outputs := []ort.Value{nil, nil}
		err = session.Run([]ort.Value{input, stateInput, rate}, outputs)
		_ = input.Destroy()
		_ = stateInput.Destroy()
		_ = rate.Destroy()
		if err != nil {
			return nil, err
		}
		probTensor, ok := outputs[0].(*ort.Tensor[float32])
		if !ok || len(probTensor.GetData()) == 0 {
			destroyValues(outputs)
			return nil, errors.New("unexpected VAD output")
		}
		prob := probTensor.GetData()[0]
		if next, ok := outputs[1].(*ort.Tensor[float32]); ok {
			state = append(state[:0], next.GetData()...)
		}
		destroyValues(outputs)
		if !active && prob >= float32(cfg.VADThreshold) {
			active, start = true, max(0, frameStart-480)
		} else if active && prob < negative {
			end := min(len(samples), frameStart+480)
			if end-start >= cfg.VADMinSpeechMS*16 {
				raw = append(raw, speechRegion{start, end})
			}
			active = false
		}
	}
	if active && len(samples)-start >= cfg.VADMinSpeechMS*16 {
		raw = append(raw, speechRegion{start, len(samples)})
	}
	// Join regions separated only by a short pause.
	joined := make([]speechRegion, 0, len(raw))
	for _, region := range raw {
		if len(joined) > 0 && region.start-joined[len(joined)-1].end <= cfg.VADMinSilenceMS*16 {
			joined[len(joined)-1].end = region.end
		} else {
			joined = append(joined, region)
		}
	}
	return joined, nil
}

type gigaDecoder struct {
	encoder, decoder, joint *ort.DynamicAdvancedSession
	vocab                   map[int]string
	blank                   int
}

func newGigaDecoder(dir string) (*gigaDecoder, error) {
	paths := []string{filepath.Join(dir, "v3_e2e_rnnt_encoder.int8.onnx"), filepath.Join(dir, "v3_e2e_rnnt_decoder.int8.onnx"), filepath.Join(dir, "v3_e2e_rnnt_joint.int8.onnx"), filepath.Join(dir, "v3_e2e_rnnt_vocab.txt")}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("model file not found: %s", path)
		}
	}
	enc, err := ort.NewDynamicAdvancedSession(paths[0], []string{"audio_signal", "length"}, []string{"encoded", "encoded_len"}, nil)
	if err != nil {
		return nil, fmt.Errorf("GigaAM encoder: %w", err)
	}
	dec, err := ort.NewDynamicAdvancedSession(paths[1], []string{"x", "h.1", "c.1"}, []string{"dec", "h", "c"}, nil)
	if err != nil {
		enc.Destroy()
		return nil, fmt.Errorf("GigaAM decoder: %w", err)
	}
	joint, err := ort.NewDynamicAdvancedSession(paths[2], []string{"enc", "dec"}, []string{"joint"}, nil)
	if err != nil {
		enc.Destroy()
		dec.Destroy()
		return nil, fmt.Errorf("GigaAM joint: %w", err)
	}
	vocab, blank, err := loadGigaVocab(paths[3])
	if err != nil {
		enc.Destroy()
		dec.Destroy()
		joint.Destroy()
		return nil, err
	}
	return &gigaDecoder{encoder: enc, decoder: dec, joint: joint, vocab: vocab, blank: blank}, nil
}

// Close releases the decoder ONNX sessions.
func (g *gigaDecoder) Close() {
	_ = g.encoder.Destroy()
	_ = g.decoder.Destroy()
	_ = g.joint.Destroy()
}

// Decode turns encoder frames into text with RNNT greedy decoding.
func (g *gigaDecoder) Decode(samples []float32) (string, error) {
	features, frames := inference.GigaAMFeatures(samples)
	featureTensor, _ := ort.NewTensor(ort.Shape{1, 64, int64(frames)}, features)
	lengthTensor, _ := ort.NewTensor(ort.Shape{1}, []int64{int64(frames)})
	outputs := []ort.Value{nil, nil}
	err := g.encoder.Run([]ort.Value{featureTensor, lengthTensor}, outputs)
	_ = featureTensor.Destroy()
	_ = lengthTensor.Destroy()
	if err != nil {
		return "", err
	}
	encodedTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		destroyValues(outputs)
		return "", errors.New("unexpected encoder output")
	}
	shape := encodedTensor.GetShape()
	if len(shape) != 3 {
		destroyValues(outputs)
		return "", fmt.Errorf("encoder shape %v", shape)
	}
	channels, steps := int(shape[1]), int(shape[2])
	if lens, ok := outputs[1].(*ort.Tensor[int32]); ok && len(lens.GetData()) > 0 {
		steps = min(steps, int(lens.GetData()[0]))
	}
	if lens, ok := outputs[1].(*ort.Tensor[int64]); ok && len(lens.GetData()) > 0 {
		steps = min(steps, int(lens.GetData()[0]))
	}
	encoded := append([]float32(nil), encodedTensor.GetData()...)
	destroyValues(outputs)
	stateH, stateC := make([]float32, 320), make([]float32, 320)
	var decoderOut []float32
	lastToken := g.blank
	tokens := make([]int, 0, steps)
	for t := 0; t < steps; t++ {
		emitted := 0
		for {
			if decoderOut == nil {
				var decodeErr error
				decoderOut, stateH, stateC, decodeErr = g.runDecoder(lastToken, stateH, stateC)
				if decodeErr != nil {
					return "", decodeErr
				}
			}
			encFrame := make([]float32, channels)
			for c := 0; c < channels; c++ {
				encFrame[c] = encoded[c*int(shape[2])+t]
			}
			logits, jointErr := g.runJoint(encFrame, decoderOut)
			if jointErr != nil {
				return "", jointErr
			}
			token := argmax(logits)
			if token == g.blank || emitted == 3 {
				break
			}
			tokens = append(tokens, token)
			lastToken = token
			decoderOut = nil
			emitted++
		}
	}
	var b strings.Builder
	for _, token := range tokens {
		b.WriteString(g.vocab[token])
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(b.String(), "▁", " ")), " "), nil
}

func (g *gigaDecoder) runDecoder(token int, h, c []float32) ([]float32, []float32, []float32, error) {
	x, _ := ort.NewTensor(ort.Shape{1, 1}, []int64{int64(token)})
	hIn, _ := ort.NewTensor(ort.Shape{1, 1, 320}, h)
	cIn, _ := ort.NewTensor(ort.Shape{1, 1, 320}, c)
	out := []ort.Value{nil, nil, nil}
	err := g.decoder.Run([]ort.Value{x, hIn, cIn}, out)
	_ = x.Destroy()
	_ = hIn.Destroy()
	_ = cIn.Destroy()
	if err != nil {
		return nil, nil, nil, err
	}
	values := make([][]float32, 3)
	for i := range out {
		t, ok := out[i].(*ort.Tensor[float32])
		if !ok {
			destroyValues(out)
			return nil, nil, nil, errors.New("unexpected decoder output")
		}
		values[i] = append([]float32(nil), t.GetData()...)
	}
	destroyValues(out)
	return values[0], values[1], values[2], nil
}

func (g *gigaDecoder) runJoint(enc, dec []float32) ([]float32, error) {
	encIn, _ := ort.NewTensor(ort.Shape{1, int64(len(enc)), 1}, enc)
	decIn, _ := ort.NewTensor(ort.Shape{1, int64(len(dec)), 1}, dec)
	out := []ort.Value{nil}
	err := g.joint.Run([]ort.Value{encIn, decIn}, out)
	_ = encIn.Destroy()
	_ = decIn.Destroy()
	if err != nil {
		return nil, err
	}
	t, ok := out[0].(*ort.Tensor[float32])
	if !ok {
		destroyValues(out)
		return nil, errors.New("unexpected joint output")
	}
	result := append([]float32(nil), t.GetData()...)
	destroyValues(out)
	return result, nil
}

func loadGigaVocab(path string) (map[int]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	vocab, blank := make(map[int]string), -1
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		split := strings.LastIndexByte(line, ' ')
		if split <= 0 {
			continue
		}
		id, parseErr := strconv.Atoi(strings.TrimSpace(line[split+1:]))
		if parseErr != nil {
			continue
		}
		token := line[:split]
		vocab[id] = token
		if token == "<blk>" || token == "<blank>" {
			blank = id
		}
	}
	if err := s.Err(); err != nil {
		return nil, 0, err
	}
	if blank < 0 {
		keys := make([]int, 0, len(vocab))
		for id := range vocab {
			keys = append(keys, id)
		}
		sort.Ints(keys)
		if len(keys) == 0 {
			return nil, 0, errors.New("empty GigaAM vocabulary")
		}
		blank = keys[len(keys)-1]
	}
	return vocab, blank, nil
}

func argmax(values []float32) int {
	best := 0
	for i := 1; i < len(values); i++ {
		if values[i] > values[best] || math.IsNaN(float64(values[best])) {
			best = i
		}
	}
	return best
}
func destroyValues(values []ort.Value) {
	for _, value := range values {
		if value != nil {
			_ = value.Destroy()
		}
	}
}
