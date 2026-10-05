package pipeline

import (
	"sort"
	"strings"
	"unicode"

	"localmeetassist/internal/model"
)

func deduplicateEcho(microphone, system []model.Segment, toleranceMS int64, threshold float64) ([]model.Segment, []model.Segment) {
	kept := make([]model.Segment, 0, len(microphone))
	dropped := make([]model.Segment, 0)
	for _, mic := range microphone {
		isEcho := false
		var nearby strings.Builder
		for _, sys := range system {
			if !timeRangesNear(mic.StartMS, mic.EndMS, sys.StartMS, sys.EndMS, toleranceMS) {
				continue
			}
			if nearby.Len() > 0 {
				nearby.WriteByte(' ')
			}
			nearby.WriteString(sys.Text)
			if echoTextMatch(mic.Text, sys.Text, threshold) {
				isEcho = true
				break
			}
		}
		// ASR engines do not necessarily choose the same segment boundaries for
		// the direct and acoustic copies. Compare the microphone phrase with the
		// concatenation of all nearby system segments as a second pass.
		if !isEcho && nearby.Len() > 0 {
			isEcho = echoTextMatch(mic.Text, nearby.String(), threshold)
		}
		if isEcho {
			dropped = append(dropped, mic)
		} else {
			kept = append(kept, mic)
		}
	}
	return kept, dropped
}

func echoTextMatch(left, right string, threshold float64) bool {
	a := transcriptWords(left)
	b := transcriptWords(right)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) <= 2 || len(b) <= 2 {
		return strings.Join(a, " ") == strings.Join(b, " ")
	}
	if transcriptSimilarity(left, right) >= threshold {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, word := range a {
		counts[word]++
	}
	common := 0
	for _, word := range b {
		if counts[word] > 0 {
			common++
			counts[word]--
		}
	}
	shorter := len(a)
	if len(b) < shorter {
		shorter = len(b)
	}
	// Containment catches a truncated microphone hypothesis that is part of a
	// longer, clean system hypothesis. Requiring three shared words prevents
	// short acknowledgements such as "да" from deleting genuine local speech.
	return common >= 3 && float64(common)/float64(shorter) >= maxFloat(threshold, 0.80)
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func timeRangesNear(aStart, aEnd, bStart, bEnd, tolerance int64) bool {
	if aEnd < aStart {
		aEnd = aStart
	}
	if bEnd < bStart {
		bEnd = bStart
	}
	return aEnd+tolerance >= bStart && bEnd+tolerance >= aStart
}

func transcriptSimilarity(left, right string) float64 {
	a := transcriptWords(left)
	b := transcriptWords(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	counts := make(map[string]int, len(a))
	for _, word := range a {
		counts[word]++
	}
	common := 0
	for _, word := range b {
		if counts[word] > 0 {
			common++
			counts[word]--
		}
	}
	// Sørensen-Dice over word multisets is intentionally tolerant to one or
	// two recognition differences between the direct and acoustic-echo copy.
	return 2 * float64(common) / float64(len(a)+len(b))
}

func transcriptWords(value string) []string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if r == 'ё' {
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

// transcriptLooksDegenerate catches a common Whisper hallucination on noise:
// the same short credits/subtitle phrase is emitted again and again.
func transcriptLooksDegenerate(segments []model.Segment) bool {
	if len(segments) < 4 {
		return false
	}
	counts := make(map[string]int)
	maxCount := 0
	for _, segment := range segments {
		key := strings.Join(transcriptWords(segment.Text), " ")
		if key == "" {
			continue
		}
		counts[key]++
		if counts[key] > maxCount {
			maxCount = counts[key]
		}
	}
	return maxCount >= 3 && maxCount*2 >= len(segments)
}

// suppressRepeatedHallucinations removes the runaway short phrase pattern
// Whisper sometimes emits over long pauses. It only targets a short phrase
// that dominates the result or occurs at least ten times: two legitimate
// repetitions remain intact, while dozens of identical guesses do not fill
// the final transcript.
func suppressRepeatedHallucinations(segments []model.Segment, maxOccurrences int) ([]model.Segment, int) {
	if maxOccurrences < 1 || len(segments) < 5 {
		return segments, 0
	}
	counts := make(map[string]int)
	wordCounts := make(map[string]int)
	for _, segment := range segments {
		words := transcriptWords(segment.Text)
		key := strings.Join(words, " ")
		if key != "" {
			counts[key]++
			wordCounts[key] = len(words)
		}
	}
	seen := make(map[string]int)
	kept := make([]model.Segment, 0, len(segments))
	dropped := 0
	for _, segment := range segments {
		key := strings.Join(transcriptWords(segment.Text), " ")
		// A phrase is suspicious when it dominates at least a third of a short
		// transcript or appears ten or more times anywhere in a long meeting.
		// Restrict this to short phrases so repeated technical explanations are
		// not collapsed accidentally.
		suspicious := wordCounts[key] <= 8 && counts[key] >= 5 && (counts[key]*3 >= len(segments) || counts[key] >= 10)
		if suspicious && seen[key] >= maxOccurrences {
			dropped++
			continue
		}
		seen[key]++
		kept = append(kept, segment)
	}
	return kept, dropped
}

// collapseRepeatedTokenRuns handles a different Whisper failure mode: one
// timestamped segment containing the same token/short phrase tens of times.
// It deliberately requires a long consecutive run, so ordinary repetitions
// and hesitations are preserved.
func collapseRepeatedTokenRuns(segments []model.Segment, keep, trigger int) ([]model.Segment, int) {
	if keep < 1 || trigger <= keep {
		return segments, 0
	}
	result := append([]model.Segment(nil), segments...)
	dropped := 0
	for index := range result {
		fields := strings.Fields(result[index].Text)
		if len(fields) < trigger {
			continue
		}
		out := make([]string, 0, len(fields))
		for pos := 0; pos < len(fields); {
			key := strings.Join(transcriptWords(fields[pos]), " ")
			if key == "" {
				out = append(out, fields[pos])
				pos++
				continue
			}
			end := pos + 1
			for end < len(fields) && strings.Join(transcriptWords(fields[end]), " ") == key {
				end++
			}
			count := end - pos
			if count >= trigger {
				out = append(out, fields[pos:pos+keep]...)
				dropped += count - keep
			} else {
				out = append(out, fields[pos:end]...)
			}
			pos = end
		}
		result[index].Text = strings.Join(out, " ")
	}
	return result, dropped
}

// suppressDegenerateRuns removes consecutive Whisper hallucinations such as
// dozens of "у-у" / "шоу, шоу" segments at the end of a long quiet track.
// A single repeated phrase is retained; only a continuous run is removed.
func suppressDegenerateRuns(segments []model.Segment, minRun int) ([]model.Segment, int) {
	if minRun < 2 || len(segments) < minRun {
		return segments, 0
	}
	isDegenerate := func(segment model.Segment) bool {
		words := transcriptWords(segment.Text)
		if len(words) < 2 {
			return false
		}
		counts := make(map[string]int)
		largest := 0
		for _, word := range words {
			counts[word]++
			if counts[word] > largest {
				largest = counts[word]
			}
		}
		return largest >= 2 && largest*5 >= len(words)*3
	}
	kept := make([]model.Segment, 0, len(segments))
	dropped := 0
	for start := 0; start < len(segments); {
		if !isDegenerate(segments[start]) {
			kept = append(kept, segments[start])
			start++
			continue
		}
		end := start + 1
		for end < len(segments) && isDegenerate(segments[end]) {
			end++
		}
		if end-start >= minRun {
			dropped += end - start
		} else {
			kept = append(kept, segments[start:end]...)
		}
		start = end
	}
	return kept, dropped
}

// markMicrophoneOwner restores the local speaker label after a single mixed
// transcription. It compares only nearby segments from the direct microphone
// transcript; all other mixed segments keep their diarization speaker.
func markMicrophoneOwner(mixed, microphone []model.Segment, toleranceMS int64, threshold float64) {
	for index := range mixed {
		for _, mic := range microphone {
			if !timeRangesNear(mixed[index].StartMS, mixed[index].EndMS, mic.StartMS, mic.EndMS, toleranceMS) {
				continue
			}
			if transcriptSimilarity(mixed[index].Text, mic.Text) >= threshold {
				mixed[index].SpeakerID = "microphone_owner"
				mixed[index].Source = "microphone"
				break
			}
		}
	}
}

func assignSystemSpeakers(segments []model.Segment, turns []SpeakerTurn) []model.Segment {
	output := append([]model.Segment(nil), segments...)
	for index := range output {
		bestSpeaker := "SPEAKER_00"
		bestOverlap := int64(0)
		bestDistance := int64(1<<63 - 1)
		segmentCenter := (output[index].StartMS + output[index].EndMS) / 2
		for _, turn := range turns {
			overlap := intervalOverlap(output[index].StartMS, output[index].EndMS, turn.StartMS, turn.EndMS)
			turnCenter := (turn.StartMS + turn.EndMS) / 2
			distance := abs64(segmentCenter - turnCenter)
			if overlap > bestOverlap || (bestOverlap == 0 && overlap == 0 && distance < bestDistance) {
				bestSpeaker = turn.SpeakerID
				bestOverlap = overlap
				bestDistance = distance
			}
		}
		output[index].SpeakerID = bestSpeaker
	}
	return output
}

func intervalOverlap(aStart, aEnd, bStart, bEnd int64) int64 {
	start := aStart
	if bStart > start {
		start = bStart
	}
	end := aEnd
	if bEnd < end {
		end = bEnd
	}
	if end <= start {
		return 0
	}
	return end - start
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// mergeSegments interleaves microphone and system segments by start time.
func mergeSegments(microphone, system []model.Segment) []model.Segment {
	segments := append(append([]model.Segment(nil), microphone...), system...)
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].StartMS == segments[j].StartMS {
			return segments[i].Source == "microphone"
		}
		return segments[i].StartMS < segments[j].StartMS
	})
	return segments
}
