package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/transcript"
	"localmeetassist/internal/uuidv7"
)

// mergeMeetings creates a new meeting first and replaces its sources only after
// all files have been prepared successfully.
func (s *Server) mergeMeetings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request struct {
		MeetingUIDs []string `json:"meeting_uids"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	uids := uniqueStrings(request.MeetingUIDs)
	if len(uids) < 2 || len(uids) > 20 {
		writeError(w, http.StatusBadRequest, "select between 2 and 20 meetings")
		return
	}
	meetings := make([]model.Meeting, 0, len(uids))
	for _, uid := range uids {
		meeting, err := s.store.Meeting(uid)
		if err != nil {
			writeError(w, http.StatusNotFound, "meeting not found: "+uid)
			return
		}
		if meeting.Status == "recording" || meeting.Status == "starting" || meeting.Status == "processing" || s.pipeline.IsRunning(uid) {
			writeError(w, http.StatusConflict, "stop recording and processing before merging meetings")
			return
		}
		meetings = append(meetings, meeting)
	}
	sort.Slice(meetings, func(i, j int) bool { return meetings[i].StartedAt.Before(meetings[j].StartedAt) })
	merged, artifacts, speakers, segments, err := s.buildMergedMeeting(meetings)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	mergedDir := s.meetingDir(merged)
	removed, err := s.store.MergeMeetingState(merged, artifacts, speakers, segments, uids)
	if err != nil {
		_ = os.RemoveAll(mergedDir)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, source := range meetings {
		dir := s.meetingDir(source)
		if s.safeManagedPath(dir) {
			_ = os.RemoveAll(dir)
		}
	}
	// Files outside a standard meeting directory are removed only when they are
	// managed and were not already removed with their parent directory.
	for _, artifact := range removed {
		if s.safeManagedPath(artifact.Path) {
			_ = os.Remove(artifact.Path)
		}
	}
	s.logger.Printf("meetings merged uid=%s sources=%d duration_ms=%d artifacts=%d", merged.UID, len(meetings), merged.DurationMS, len(artifacts))
	writeJSON(w, http.StatusCreated, merged)
}

func (s *Server) buildMergedMeeting(sources []model.Meeting) (model.Meeting, []model.Artifact, []model.Speaker, []model.Segment, error) {
	first := sources[0]
	started := first.StartedAt
	finished := started
	for _, source := range sources {
		end := source.StartedAt.Add(time.Duration(source.DurationMS) * time.Millisecond)
		if source.FinishedAt != nil && source.FinishedAt.After(end) {
			end = *source.FinishedAt
		}
		if end.After(finished) {
			finished = end
		}
	}
	now := time.Now()
	title := strings.TrimSpace(first.Title)
	if title == "" {
		title = config.Localized(s.config().App.Language, "Объединённая встреча", "Merged meeting")
	}
	merged := model.Meeting{
		UID: uuidv7.New(), Title: title, StartedAt: started, FinishedAt: &finished,
		DurationMS: finished.Sub(started).Milliseconds(), Status: "completed", Source: "merged",
		InputDevice: first.InputDevice, OutputDevice: first.OutputDevice, SummaryStatus: "pending",
		Metadata:  map[string]string{"merged_from": strings.Join(meetingUIDs(sources), ","), "microphone_owner_name": s.ownerName()},
		CreatedAt: now, UpdatedAt: now, ProcessingStage: "completed",
	}
	if owner := first.Metadata["microphone_owner_name"]; owner != "" {
		merged.Metadata["microphone_owner_name"] = owner
	}
	mergedDir := s.meetingDir(merged)
	if err := os.MkdirAll(mergedDir, 0o700); err != nil {
		return merged, nil, nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(mergedDir)
		}
	}()

	speakers, segments, names, err := s.mergeMeetingSpeakers(sources, merged.UID, started)
	if err != nil {
		return merged, nil, nil, nil, err
	}
	merged.SpeakerCount = len(speakers)
	merged.Transcript = transcript.Format(segments, names)
	partLabel := config.Localized(s.config().App.Language, "ЧАСТЬ", "PART")
	merged.Summary = mergeSummaries(sources, started, partLabel, s.config().App.Language)
	if strings.TrimSpace(merged.Summary) != "" {
		merged.SummaryStatus = "completed"
	}
	if len(speakers) > 0 {
		merged.Metadata["participant_count"] = strconv.Itoa(len(speakers))
	}

	artifacts := make([]model.Artifact, 0)
	if err := s.mergeMeetingAudio(sources, started, mergedDir, merged.UID, &artifacts); err != nil {
		return merged, nil, nil, nil, err
	}
	if err := s.copyMergedSourceArtifacts(sources, mergedDir, merged.UID, &artifacts); err != nil {
		return merged, nil, nil, nil, err
	}
	if merged.Transcript != "" {
		path := filepath.Join(mergedDir, "transcript", "transcript.md")
		if err := writeMergedText(path, merged.UID, "transcript", merged.Transcript); err != nil {
			return merged, nil, nil, nil, err
		}
		artifact, err := artifactRecord(merged.UID, "transcript", path, "text/markdown", "merged")
		if err != nil {
			return merged, nil, nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	if merged.Summary != "" {
		path := filepath.Join(mergedDir, "summary", "summary.md")
		if err := writeMergedText(path, merged.UID, "summary", merged.Summary); err != nil {
			return merged, nil, nil, nil, err
		}
		artifact, err := artifactRecord(merged.UID, "summary", path, "text/markdown", "merged")
		if err != nil {
			return merged, nil, nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	metadataPath := filepath.Join(mergedDir, "metadata.json")
	metadata, _ := json.MarshalIndent(map[string]any{"schema_version": 1, "meeting_uid": merged.UID, "title": merged.Title, "started_at": merged.StartedAt, "finished_at": merged.FinishedAt, "status": merged.Status, "merged_from": meetingUIDs(sources)}, "", "  ")
	if err := os.WriteFile(metadataPath, append(metadata, '\n'), 0o600); err != nil {
		return merged, nil, nil, nil, err
	}
	metadataArtifact, err := artifactRecord(merged.UID, "metadata", metadataPath, "application/json", "merged")
	if err != nil {
		return merged, nil, nil, nil, err
	}
	artifacts = append(artifacts, metadataArtifact)
	ok = true
	return merged, artifacts, speakers, segments, nil
}

func (s *Server) mergeMeetingAudio(sources []model.Meeting, origin time.Time, mergedDir, meetingUID string, artifacts *[]model.Artifact) error {
	clips := map[string][]audio.TimelineClip{"mic_wav": {}, "system_wav": {}}
	for _, source := range sources {
		byType := make(map[string]string)
		sourceArtifacts, _ := s.store.Artifacts(source.UID)
		for _, artifact := range sourceArtifacts {
			if artifact.Type == "mic_wav" || artifact.Type == "system_wav" {
				byType[artifact.Type] = artifact.Path
			}
		}
		for kind, name := range map[string]string{"mic_wav": "microphone.wav", "system_wav": "system.wav"} {
			path := byType[kind]
			if path == "" {
				path = filepath.Join(s.meetingDir(source), "audio", name)
			}
			if validWAVFile(path) {
				clips[kind] = append(clips[kind], audio.TimelineClip{Path: path, StartMS: source.StartedAt.Sub(origin).Milliseconds()})
			}
		}
	}
	if len(clips["mic_wav"]) == 0 && len(clips["system_wav"]) == 0 {
		return nil
	}
	audioDir := filepath.Join(mergedDir, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		return err
	}
	micPath, systemPath := filepath.Join(audioDir, "microphone.wav"), filepath.Join(audioDir, "system.wav")
	if len(clips["mic_wav"]) > 0 {
		if err := audio.MergePCM16WAVTimeline(clips["mic_wav"], micPath); err != nil {
			return fmt.Errorf("merge microphone audio: %w", err)
		}
	}
	if len(clips["system_wav"]) > 0 {
		if err := audio.MergePCM16WAVTimeline(clips["system_wav"], systemPath); err != nil {
			return fmt.Errorf("merge system audio: %w", err)
		}
	}
	if len(clips["mic_wav"]) == 0 {
		if err := audio.CreateSilentPCM16WAVLike(systemPath, micPath); err != nil {
			return err
		}
	}
	if len(clips["system_wav"]) == 0 {
		if err := audio.CreateSilentPCM16WAVLike(micPath, systemPath); err != nil {
			return err
		}
	}
	mixedPath := filepath.Join(audioDir, "mixed.wav")
	cfg := s.config()
	if err := audio.MixPCM16WAV(micPath, systemPath, mixedPath, cfg.Audio.MicrophoneGain, cfg.Audio.SystemGain); err != nil {
		return err
	}
	for _, item := range []struct{ kind, path string }{{"mic_wav", micPath}, {"system_wav", systemPath}, {"mixed_wav", mixedPath}} {
		artifact, err := artifactRecord(meetingUID, item.kind, item.path, "audio/wav", "merged")
		if err != nil {
			return err
		}
		*artifacts = append(*artifacts, artifact)
	}
	return nil
}

func (s *Server) copyMergedSourceArtifacts(sources []model.Meeting, mergedDir, meetingUID string, output *[]model.Artifact) error {
	skip := map[string]bool{"mic_wav": true, "system_wav": true, "mixed_wav": true}
	for index, source := range sources {
		artifacts, err := s.store.Artifacts(source.UID)
		if err != nil {
			return err
		}
		for _, artifact := range artifacts {
			if skip[artifact.Type] || !s.safeManagedPath(artifact.Path) {
				continue
			}
			if info, err := os.Stat(artifact.Path); err != nil || !info.Mode().IsRegular() {
				continue
			}
			dir := filepath.Join(mergedDir, "sources", fmt.Sprintf("%02d-%s", index+1, shortUID(source.UID)))
			destination := filepath.Join(dir, shortUID(artifact.UID)+"-"+sanitizeName(filepath.Base(artifact.Path)))
			if err := copyFile(artifact.Path, destination); err != nil {
				return err
			}
			kind := "source_" + artifact.Type
			record, err := artifactRecord(meetingUID, kind, destination, artifact.MIMEType, "merged_source")
			if err != nil {
				return err
			}
			*output = append(*output, record)
		}
	}
	return nil
}

func (s *Server) mergeMeetingSpeakers(sources []model.Meeting, meetingUID string, origin time.Time) ([]model.Speaker, []model.Segment, map[string]string, error) {
	var speakers []model.Speaker
	var segments []model.Segment
	names := make(map[string]string)
	byKey := make(map[string]string)
	for meetingIndex, source := range sources {
		offset := source.StartedAt.Sub(origin).Milliseconds()
		sourceSpeakers, err := s.store.Speakers(source.UID)
		if err != nil {
			return nil, nil, nil, err
		}
		mapping := make(map[string]string)
		for _, speaker := range sourceSpeakers {
			name := strings.TrimSpace(speaker.DisplayName)
			if name == "" {
				name = speaker.ID
			}
			technical := strings.HasPrefix(strings.ToUpper(name), "SPEAKER_")
			key := speaker.Source + "|" + strings.ToLower(name)
			if speaker.Source == "microphone" {
				key = "microphone"
			} else if technical {
				key = fmt.Sprintf("%d|%s", meetingIndex, key)
			}
			id := byKey[key]
			if id == "" {
				id = fmt.Sprintf("MERGED_%02d", len(speakers)+1)
				byKey[key] = id
				speakers = append(speakers, model.Speaker{MeetingUID: meetingUID, ID: id, DisplayName: name, Source: speaker.Source})
				names[id] = name
			}
			mapping[speaker.ID] = id
			for idx := range speakers {
				if speakers[idx].ID != id {
					continue
				}
				speakers[idx].Samples = append(speakers[idx].Samples, offsetSamples(speaker.Samples, offset, fmt.Sprintf("%d-", meetingIndex))...)
				speakers[idx].Fragments = append(speakers[idx].Fragments, offsetSamples(speaker.Fragments, offset, fmt.Sprintf("%d-", meetingIndex))...)
			}
		}
		sourceSegments, err := s.store.TranscriptSegments(source.UID)
		if err != nil {
			sourceSegments = parseTranscriptSegments(source.Transcript)
		}
		for index, segment := range sourceSegments {
			id := mapping[segment.SpeakerID]
			if id == "" {
				key := fmt.Sprintf("%d|%s|%s", meetingIndex, segment.Source, segment.SpeakerID)
				id = byKey[key]
				if id == "" {
					id = fmt.Sprintf("MERGED_%02d", len(speakers)+1)
					byKey[key] = id
					name := segment.SpeakerID
					speakers = append(speakers, model.Speaker{MeetingUID: meetingUID, ID: id, DisplayName: name, Source: segment.Source})
					names[id] = name
				}
			}
			segment.ID = fmt.Sprintf("%d-%d-%s", meetingIndex, index, segment.ID)
			segment.StartMS += offset
			segment.EndMS += offset
			segment.SpeakerID = id
			segments = append(segments, segment)
		}
	}
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].StartMS < segments[j].StartMS })
	return speakers, segments, names, nil
}

func parseTranscriptSegments(value string) []model.Segment {
	var output []model.Segment
	for index, line := range strings.Split(value, "\n") {
		// Общий шаблон (см. reconcile.go): группы — минуты, секунды, опциональные
		// доли, затем имя и текст.
		match := transcriptLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 6 {
			continue
		}
		minutes, _ := strconv.ParseInt(match[1], 10, 64)
		seconds, _ := strconv.ParseInt(match[2], 10, 64)
		start := (minutes*60 + seconds) * 1000
		output = append(output, model.Segment{
			ID: fmt.Sprintf("parsed-%d", index), StartMS: start, EndMS: start + 10000,
			SpeakerID: strings.TrimSpace(match[4]), Source: "unknown", Text: strings.TrimSpace(match[5]),
		})
	}
	return output
}

func offsetSamples(samples []model.Sample, offset int64, prefix string) []model.Sample {
	output := make([]model.Sample, len(samples))
	for index, sample := range samples {
		output[index] = sample
		output[index].ID = prefix + sample.ID
		output[index].StartMS += offset
		output[index].EndMS += offset
	}
	return output
}

// mergeOffsetLabel renders a wall-clock offset the way it reads in a summary,
// instead of the raw Go duration "1h5m0s".
func mergeOffsetLabel(lang string, d time.Duration) string {
	total := int(d / time.Second)
	if total < 60 {
		return config.Localized(lang, fmt.Sprintf("%d сек", total), fmt.Sprintf("%ds", total))
	}
	minutes := total / 60
	seconds := total % 60
	if seconds == 0 {
		return config.Localized(lang, fmt.Sprintf("%d мин", minutes), fmt.Sprintf("%dm", minutes))
	}
	return config.Localized(lang, fmt.Sprintf("%d мин %d сек", minutes, seconds), fmt.Sprintf("%dm %ds", minutes, seconds))
}

func mergeSummaries(sources []model.Meeting, origin time.Time, partLabel, lang string) string {
	var parts []string
	for _, source := range sources {
		if strings.TrimSpace(source.Summary) == "" {
			continue
		}
		offset := source.StartedAt.Sub(origin).Round(time.Second)
		parts = append(parts, fmt.Sprintf("%s %s (+%s)\n%s", partLabel, source.Title, mergeOffsetLabel(lang, offset), strings.TrimSpace(source.Summary)))
	}
	return strings.Join(parts, "\n\n")
}

func writeMergedText(path, uid, kind, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("---\nmeeting_uid: %s\nartifact_type: %s\ncreated_at: %s\nschema_version: 1\n---\n\n%s\n", uid, kind, time.Now().UTC().Format(time.RFC3339Nano), content)
	return os.WriteFile(path, []byte(body), 0o600)
}

func artifactRecord(meetingUID, kind, path, mimeType, source string) (model.Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.Artifact{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return model.Artifact{}, err
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(path))
	}
	return model.Artifact{UID: uuidv7.New(), MeetingUID: meetingUID, Type: kind, Path: path, MIMEType: mimeType, SizeBytes: size, SHA256: hex.EncodeToString(hash.Sum(nil)), Source: source, CreatedAt: time.Now()}, nil
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	var output []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			output = append(output, value)
		}
	}
	return output
}
func meetingUIDs(meetings []model.Meeting) []string {
	output := make([]string, len(meetings))
	for index := range meetings {
		output[index] = meetings[index].UID
	}
	return output
}
func shortUID(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}
