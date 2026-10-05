package pipeline

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	ort "github.com/yalue/onnxruntime_go"

	"localmeetassist/internal/config"
	"localmeetassist/internal/inference"
	"localmeetassist/internal/modelmanager"
)

// SpeakerTurn is one diarized speech interval assigned to a speaker.
type SpeakerTurn struct {
	StartMS   int64  `json:"start_ms"`
	EndMS     int64  `json:"end_ms"`
	SpeakerID string `json:"speaker_id"`
}

type segmentationWindow struct {
	startSample int
	frames      int
	probability []float32
}

type speakerObservation struct {
	window, local int
	embedding     []float32
	valid         bool
}

func diarizeWithEngine(ctx context.Context, runtimeConfig config.Inference, cfg config.Diarization, audioPath string, numSpeakers int, progress func(int)) ([]SpeakerTurn, error) {
	if strings.EqualFold(strings.TrimSpace(cfg.Engine), "mock") {
		return []SpeakerTurn{{StartMS: 0, EndMS: 1500, SpeakerID: "SPEAKER_00"}, {StartMS: 1500, EndMS: 60000, SpeakerID: "SPEAKER_01"}}, nil
	}
	runtimePath := modelmanager.ResolveRuntimeLibrary(runtimeConfig.RuntimePath, runtimeConfig.RuntimeVersion)
	if override := strings.TrimSpace(os.Getenv("LOCALMEETASSIST_ONNXRUNTIME_PATH")); override != "" {
		runtimePath = override
	}
	if err := inference.EnsureRuntime(runtimePath); err != nil {
		return nil, err
	}
	for name, path := range map[string]string{"PyAnnote Segmentation": cfg.SegmentationModel, "WeSpeaker": cfg.EmbeddingModel} {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%s model is unavailable: %w", name, err)
		}
	}
	audio, err := inference.LoadWAV16kMono(audioPath)
	if err != nil {
		return nil, err
	}
	windows, err := runSegmentation(ctx, cfg.SegmentationModel, audio, func(done, total int) {
		if progress != nil {
			progress(done * 45 / max(1, total))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("PyAnnote Segmentation: %w", err)
	}
	observations, err := runEmbeddings(ctx, cfg.EmbeddingModel, audio, windows, func(done, total int) {
		if progress != nil {
			progress(45 + done*45/max(1, total))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("WeSpeaker: %w", err)
	}
	labels := clusterSpeakerEmbeddings(observations, numSpeakers, cfg.ClusterThreshold)
	turns := reconstructTurns(windows, labels, len(audio))
	if progress != nil {
		progress(100)
	}
	return turns, nil
}

func runSegmentation(ctx context.Context, modelPath string, audio []float32, report func(int, int)) ([]segmentationWindow, error) {
	session, err := ort.NewDynamicAdvancedSession(modelPath, []string{"input_values"}, []string{"logits"}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Destroy()
	const windowSize, stepSize = 160000, 80000
	stop := max(1, len(audio)-80000)
	starts := make([]int, 0, 1+stop/stepSize)
	for start := 0; start < stop; start += stepSize {
		starts = append(starts, start)
	}
	if len(starts) == 0 {
		starts = append(starts, 0)
	}
	result := make([]segmentationWindow, 0, len(starts))
	for index, start := range starts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		window := make([]float32, windowSize)
		copy(window, audio[start:min(start+windowSize, len(audio))])
		input, _ := ort.NewTensor(ort.Shape{1, 1, windowSize}, window)
		outputs := []ort.Value{nil}
		runErr := session.Run([]ort.Value{input}, outputs)
		_ = input.Destroy()
		if runErr != nil {
			return nil, runErr
		}
		tensor, ok := outputs[0].(*ort.Tensor[float32])
		if !ok {
			destroyValues(outputs)
			return nil, errors.New("unexpected logits type")
		}
		shape, logits := tensor.GetShape(), tensor.GetData()
		if len(shape) != 3 || shape[2] != 7 {
			destroyValues(outputs)
			return nil, fmt.Errorf("unexpected logits shape %v", shape)
		}
		frames := int(shape[1])
		prob := make([]float32, frames*3)
		for frame := 0; frame < frames; frame++ {
			row := logits[frame*7 : frame*7+7]
			prob[frame*3] = exp32(row[1]) + exp32(row[4]) + exp32(row[5])
			prob[frame*3+1] = exp32(row[2]) + exp32(row[4]) + exp32(row[6])
			prob[frame*3+2] = exp32(row[3]) + exp32(row[5]) + exp32(row[6])
		}
		destroyValues(outputs)
		result = append(result, segmentationWindow{startSample: start, frames: frames, probability: prob})
		report(index+1, len(starts))
	}
	return result, nil
}

func runEmbeddings(ctx context.Context, modelPath string, audio []float32, windows []segmentationWindow, report func(int, int)) ([]speakerObservation, error) {
	session, err := ort.NewDynamicAdvancedSession(modelPath, []string{"feats"}, []string{"embs"}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Destroy()
	observations := make([]speakerObservation, 0, len(windows)*3)
	total, done := len(windows)*3, 0
	for windowIndex, window := range windows {
		for local := 0; local < 3; local++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			observation := speakerObservation{window: windowIndex, local: local}
			masked := make([]float32, 0, 160000)
			for frame := 0; frame < window.frames; frame++ {
				// Embeddings built from overlapping or uncertain speech often pull
				// two real people into one cluster. Use only clean, dominant frames
				// as voice anchors; the full probabilities are still used later to
				// reconstruct turns and overlaps.
				if window.probability[frame*3+local] < .60 {
					continue
				}
				overlap := false
				for other := 0; other < 3; other++ {
					if other != local && window.probability[frame*3+other] >= .45 {
						overlap = true
						break
					}
				}
				if overlap {
					continue
				}
				start := window.startSample + frame*270
				end := min(start+270, len(audio))
				if start < len(audio) && end > start {
					masked = append(masked, audio[start:end]...)
				}
			}
			if len(masked) >= 16000 {
				features, frames := inference.WeSpeakerFeatures(masked)
				if frames > 0 {
					input, _ := ort.NewTensor(ort.Shape{1, int64(frames), 80}, features)
					outputs := []ort.Value{nil}
					runErr := session.Run([]ort.Value{input}, outputs)
					_ = input.Destroy()
					if runErr != nil {
						return nil, runErr
					}
					if tensor, ok := outputs[0].(*ort.Tensor[float32]); ok {
						observation.embedding = append([]float32(nil), tensor.GetData()...)
						normalizeVector(observation.embedding)
						observation.valid = true
					} else {
						destroyValues(outputs)
						return nil, errors.New("unexpected WeSpeaker output")
					}
					destroyValues(outputs)
				}
			}
			observations = append(observations, observation)
			done++
			report(done, total)
		}
	}
	return observations, nil
}

type voiceCluster struct {
	members  []int
	centroid []float32
	windows  map[int]bool
	active   bool
	version  int
}

// clusterPair is a candidate merge of two clusters together with the cluster
// versions it was computed from, so stale candidates can be discarded.
type clusterPair struct {
	left, right               int
	leftVersion, rightVersion int
	distance                  float64
}

// clusterPairHeap is a min-heap of candidate merges ordered by cosine distance.
type clusterPairHeap []clusterPair

func (h clusterPairHeap) Len() int           { return len(h) }
func (h clusterPairHeap) Less(i, j int) bool { return h[i].distance < h[j].distance }
func (h clusterPairHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *clusterPairHeap) Push(value any)    { *h = append(*h, value.(clusterPair)) }
func (h *clusterPairHeap) Pop() any {
	old := *h
	value := old[len(old)-1]
	*h = old[:len(old)-1]
	return value
}

func clusterSpeakerEmbeddings(observations []speakerObservation, requested int, threshold float64) []int {
	labels := make([]int, len(observations))
	for i := range labels {
		labels[i] = -1
	}
	clusters := make([]voiceCluster, len(observations))
	active := 0
	for i, item := range observations {
		if !item.valid {
			continue
		}
		clusters[i] = voiceCluster{members: []int{i}, centroid: append([]float32(nil), item.embedding...), windows: map[int]bool{item.window: true}, active: true}
		active++
	}
	target := requested
	if target > active {
		target = active
	}
	pairs := &clusterPairHeap{}
	heap.Init(pairs)
	pushPair := func(left, right int) {
		if left > right {
			left, right = right, left
		}
		if left == right || !clusters[left].active || !clusters[right].active || overlapWindows(clusters[left].windows, clusters[right].windows) {
			return
		}
		heap.Push(pairs, clusterPair{left: left, right: right, leftVersion: clusters[left].version, rightVersion: clusters[right].version, distance: 1 - cosine(clusters[left].centroid, clusters[right].centroid)})
	}
	for i := range clusters {
		if clusters[i].active {
			for j := i + 1; j < len(clusters); j++ {
				pushPair(i, j)
			}
		}
	}
	for active > 1 {
		if target > 0 && active <= target {
			break
		}
		bestLeft, bestRight, bestDistance := -1, -1, math.Inf(1)
		for pairs.Len() > 0 {
			candidate := heap.Pop(pairs).(clusterPair)
			left, right := &clusters[candidate.left], &clusters[candidate.right]
			if !left.active || !right.active || left.version != candidate.leftVersion || right.version != candidate.rightVersion || overlapWindows(left.windows, right.windows) {
				continue
			}
			bestLeft, bestRight, bestDistance = candidate.left, candidate.right, candidate.distance
			break
		}
		// Never violate cannot-link for tracks observed in the same window.
		// Returning an extra Speaker is safer than silently mixing two people;
		// the UI can merge a split identity, while mixed voices are harder to
		// recover automatically.
		if bestLeft < 0 || (target <= 0 && bestDistance > threshold) {
			break
		}
		left, right := &clusters[bestLeft], &clusters[bestRight]
		left.members = append(left.members, right.members...)
		for window := range right.windows {
			left.windows[window] = true
		}
		for i := range left.centroid {
			left.centroid[i] = 0
		}
		for _, member := range left.members {
			for i, value := range observations[member].embedding {
				left.centroid[i] += value
			}
		}
		normalizeVector(left.centroid)
		left.version++
		right.active = false
		right.version++
		active--
		for index := range clusters {
			if index != bestLeft {
				pushPair(bestLeft, index)
			}
		}
	}
	var remaining []int
	for i := range clusters {
		if clusters[i].active {
			remaining = append(remaining, i)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return minSlice(clusters[remaining[i]].members) < minSlice(clusters[remaining[j]].members)
	})
	for label, index := range remaining {
		for _, member := range clusters[index].members {
			labels[member] = label
		}
	}
	return labels
}

func reconstructTurns(windows []segmentationWindow, labels []int, audioSamples int) []SpeakerTurn {
	const frameSamples = 270
	totalFrames := max(1, (audioSamples+frameSamples-1)/frameSamples)
	maxLabel := -1
	for _, label := range labels {
		if label > maxLabel {
			maxLabel = label
		}
	}
	if maxLabel < 0 {
		return nil
	}
	sums := make([][]float32, totalFrames)
	counts := make([][]int, totalFrames)
	for i := range sums {
		sums[i] = make([]float32, maxLabel+1)
		counts[i] = make([]int, maxLabel+1)
	}
	for windowIndex, window := range windows {
		base := int(math.Round(float64(window.startSample) / frameSamples))
		for label := 0; label <= maxLabel; label++ {
			for frame := 0; frame < window.frames && base+frame < totalFrames; frame++ {
				var probability float32
				used := false
				for local := 0; local < 3; local++ {
					if labels[windowIndex*3+local] == label {
						used = true
						probability = max(probability, window.probability[frame*3+local])
					}
				}
				if used {
					sums[base+frame][label] += probability
					counts[base+frame][label]++
				}
			}
		}
	}
	var turns []SpeakerTurn
	for speaker := 0; speaker <= maxLabel; speaker++ {
		active, start := false, 0
		for frame := 0; frame < totalFrames; frame++ {
			var probability float32
			if counts[frame][speaker] > 0 {
				probability = sums[frame][speaker] / float32(counts[frame][speaker])
			}
			if !active && probability >= .5 {
				active, start = true, frame
			}
			if active && probability < .35 {
				active = false
				if frame > start {
					turns = append(turns, SpeakerTurn{StartMS: int64(start*frameSamples) * 1000 / 16000, EndMS: min(int64(frame*frameSamples)*1000/16000, int64(audioSamples)*1000/16000), SpeakerID: fmt.Sprintf("SPEAKER_%02d", speaker)})
				}
			}
		}
		if active {
			turns = append(turns, SpeakerTurn{StartMS: int64(start*frameSamples) * 1000 / 16000, EndMS: int64(audioSamples) * 1000 / 16000, SpeakerID: fmt.Sprintf("SPEAKER_%02d", speaker)})
		}
	}
	sort.SliceStable(turns, func(i, j int) bool {
		if turns[i].StartMS == turns[j].StartMS {
			return turns[i].EndMS < turns[j].EndMS
		}
		return turns[i].StartMS < turns[j].StartMS
	})
	return mergeAdjacentSpeakerTurns(turns)
}

func exp32(value float32) float32 { return float32(math.Exp(float64(value))) }
func normalizeVector(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 1e-8 {
		for i := range v {
			v[i] /= norm
		}
	}
}
func cosine(a, b []float32) float64 {
	var value float64
	for i := 0; i < min(len(a), len(b)); i++ {
		value += float64(a[i] * b[i])
	}
	return value
}
func overlapWindows(a, b map[int]bool) bool {
	for key := range a {
		if b[key] {
			return true
		}
	}
	return false
}
func minSlice(values []int) int {
	result := math.MaxInt
	for _, value := range values {
		if value < result {
			result = value
		}
	}
	return result
}

func mergeAdjacentSpeakerTurns(turns []SpeakerTurn) []SpeakerTurn {
	merged := make([]SpeakerTurn, 0, len(turns))
	for _, turn := range turns {
		last := len(merged) - 1
		if last >= 0 && merged[last].SpeakerID == turn.SpeakerID && turn.StartMS <= merged[last].EndMS+250 {
			if turn.EndMS > merged[last].EndMS {
				merged[last].EndMS = turn.EndMS
			}
			continue
		}
		merged = append(merged, turn)
	}
	return merged
}
