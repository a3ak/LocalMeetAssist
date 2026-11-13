package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/model"
	"localmeetassist/internal/transcript"
	"localmeetassist/internal/uuidv7"
)

const minSplitPartMS = 60_000

type splitMeetingRequest struct {
	SplitMS     int64  `json:"split_ms"`
	FirstTitle  string `json:"first_title"`
	SecondTitle string `json:"second_title"`
}

// splitMeeting replaces one completed meeting with two parts at split_ms. The
// transcript, diarization result and speaker links are split from the stored
// layers; nothing is re-transcribed. The original meeting is removed only after
// both parts and their files are ready.
func (s *Server) splitMeeting(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.pipeline != nil && s.pipeline.IsRunning(uid) {
		writeError(w, http.StatusConflict, "meeting is being processed")
		return
	}
	meeting, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found")
		return
	}
	if meeting.Status != "completed" && meeting.Status != "warning" {
		writeError(w, http.StatusConflict, "only completed meetings can be split")
		return
	}
	var req splitMeetingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	duration := meeting.DurationMS
	if duration == 0 {
		duration = s.splitMeetingDuration(meeting)
	}
	if duration < 2*minSplitPartMS {
		writeError(w, http.StatusConflict, "meeting is too short to split")
		return
	}
	if req.SplitMS < minSplitPartMS || req.SplitMS > duration-minSplitPartMS {
		writeError(w, http.StatusBadRequest, "split point must leave at least one minute in each part")
		return
	}
	firstTitle := strings.TrimSpace(req.FirstTitle)
	secondTitle := strings.TrimSpace(req.SecondTitle)
	if firstTitle == "" {
		firstTitle = meeting.Title + " — часть 1"
	}
	if secondTitle == "" {
		secondTitle = meeting.Title + " — часть 2"
	}
	first := s.splitPartMeeting(meeting, req.SplitMS, 0, firstTitle)
	second := s.splitPartMeeting(meeting, duration-req.SplitMS, req.SplitMS, secondTitle)
	artifacts, speakers, segments, err := s.buildSplitState(meeting, &first, &second, req.SplitMS)
	if err != nil {
		s.removeSplitDirs(first, second)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	originalDir := s.meetingDir(meeting)
	backupDir := originalDir + ".split-" + shortUID(first.UID)
	renamed := false
	if info, statErr := os.Stat(originalDir); statErr == nil && info.IsDir() {
		if err := os.Rename(originalDir, backupDir); err != nil {
			s.removeSplitDirs(first, second)
			writeError(w, http.StatusInternalServerError, "cannot move the original meeting directory: "+err.Error())
			return
		}
		renamed = true
	}
	jobs := append(splitMeetingJobs(first.UID), splitMeetingJobs(second.UID)...)
	if err := s.store.SplitMeetingState(meeting.UID, first, second, artifacts, speakers, segments, jobs); err != nil {
		if renamed {
			_ = os.Rename(backupDir, originalDir)
		}
		s.removeSplitDirs(first, second)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if renamed {
		if err := os.RemoveAll(backupDir); err != nil {
			s.logger.Printf("split: original directory was not removed uid=%s error=%v", meeting.UID, err)
		}
	}
	s.logger.Printf("meeting split uid=%s first=%s second=%s split_ms=%d", meeting.UID, first.UID, second.UID, req.SplitMS)
	writeJSON(w, http.StatusOK, map[string]any{"first": first, "second": second})
}

func (s *Server) splitPartMeeting(original model.Meeting, duration, startMS int64, title string) model.Meeting {
	now := time.Now()
	started := original.StartedAt.Add(time.Duration(startMS) * time.Millisecond)
	finished := original.StartedAt.Add(time.Duration(startMS+duration) * time.Millisecond)
	status := original.Status
	if status != "warning" {
		status = "completed"
	}
	part := original
	part.UID = uuidv7.New()
	part.Title = title
	part.StartedAt = started
	part.FinishedAt = &finished
	part.DurationMS = duration
	part.Status = status
	part.ProcessingStage = "completed"
	part.LastError = ""
	part.Transcript = ""
	part.Summary = ""
	part.SummaryStatus = "pending"
	part.ArtifactUIDs = nil
	part.SpeakerCount = 0
	part.CreatedAt = now
	part.UpdatedAt = now
	part.Metadata = cloneMetadata(original.Metadata)
	delete(part.Metadata, "merged_from")
	delete(part.Metadata, "summary_stale")
	part.Metadata["split_from"] = original.UID
	part.Metadata["split_at_ms"] = fmt.Sprintf("%d", startMS)
	return part
}

func cloneMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *Server) splitMeetingDuration(meeting model.Meeting) int64 {
	var max int64
	dir := s.meetingDir(meeting)
	for _, name := range []string{"microphone.wav", "system.wav", "mixed.wav"} {
		if duration, err := audio.PCM16WAVDurationMS(filepath.Join(dir, "audio", name)); err == nil && duration > max {
			max = duration
		}
	}
	return max
}

func (s *Server) removeSplitDirs(parts ...model.Meeting) {
	for _, part := range parts {
		if dir := s.meetingDir(part); s.safeManagedPath(dir) {
			_ = os.RemoveAll(dir)
		}
	}
}

func (s *Server) buildSplitState(original model.Meeting, first, second *model.Meeting, splitMS int64) ([]model.Artifact, []model.Speaker, map[string][]model.Segment, error) {
	segments, err := s.store.TranscriptSegments(original.UID)
	if err != nil {
		segments = parseTranscriptSegments(original.Transcript)
	}
	firstSegments, secondSegments := splitMeetingSegments(segments, splitMS)
	originalSpeakers, err := s.store.Speakers(original.UID)
	if err != nil {
		return nil, nil, nil, err
	}
	previous := make(map[string]model.Speaker, len(originalSpeakers))
	for _, speaker := range originalSpeakers {
		previous[speaker.ID] = speaker
	}
	owner := s.ownerName()
	firstSpeakers := buildSpeakersFromSegments(first.UID, firstSegments, previous, owner)
	secondSpeakers := buildSpeakersFromSegments(second.UID, secondSegments, previous, owner)
	firstNames := speakerNames(firstSpeakers)
	secondNames := speakerNames(secondSpeakers)
	first.Transcript = transcript.Format(firstSegments, firstNames)
	second.Transcript = transcript.Format(secondSegments, secondNames)
	first.SpeakerCount = len(firstSpeakers)
	second.SpeakerCount = len(secondSpeakers)

	artifacts := make([]model.Artifact, 0)
	if err := s.splitMeetingAudio(original, *first, *second, splitMS, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	if err := s.writeSplitTranscript(original, *first, firstSegments, first.Transcript, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	if err := s.writeSplitTranscript(original, *second, secondSegments, second.Transcript, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	if err := s.splitRawTranscripts(original, *first, *second, splitMS, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	if err := s.splitDiarizationArtifact(original, *first, *second, splitMS, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	if err := s.copySplitAttachments(original, *first, *second, &artifacts); err != nil {
		return nil, nil, nil, err
	}
	speakers := append(append([]model.Speaker(nil), firstSpeakers...), secondSpeakers...)
	return artifacts, speakers, map[string][]model.Segment{first.UID: firstSegments, second.UID: secondSegments}, nil
}

// BackfillSplitJobs restores the processing rows for meetings split before
// jobs were copied into the parts. It only touches meetings marked with
// split_from and leaves meetings that already have jobs untouched.
func (s *Server) BackfillSplitJobs() int {
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		s.logger.Printf("split job backfill: list meetings: %v", err)
		return 0
	}
	repaired := 0
	for _, meeting := range meetings {
		if meeting.Metadata["split_from"] == "" {
			continue
		}
		jobs, err := s.store.Jobs(meeting.UID)
		if err != nil {
			continue
		}
		existing := make(map[string]bool, len(jobs))
		for _, job := range jobs {
			existing[job.Stage] = true
		}
		added := 0
		for _, job := range splitMeetingJobs(meeting.UID) {
			if existing[job.Stage] {
				continue
			}
			if err := s.store.SaveJob(job); err != nil {
				s.logger.Printf("split job backfill: save %s/%s: %v", meeting.UID, job.Stage, err)
				continue
			}
			added++
		}
		if added == 0 {
			continue
		}
		if meeting.SummaryStatus == "disabled" && meeting.Summary == "" {
			meeting.SummaryStatus = "pending"
			meeting.UpdatedAt = time.Now()
			_ = s.store.SaveMeeting(meeting)
		}
		repaired++
	}
	return repaired
}

// splitMeetingJobs initializes the processing tab for a split part. Transcription
// and diarization were carried over from the source, while summary, matching,
// enrollment and audio archiving still need to be run manually.
func splitMeetingJobs(meetingUID string) []model.Job {
	now := time.Now()
	statuses := []struct {
		stage  string
		status string
	}{
		{"transcribing", "completed"},
		{"diarizing", "completed"},
		{"deduplicating", "completed"},
		{"merging", "completed"},
		{"matching", "not_started"},
		{"summarizing", "not_started"},
		{"enrolling", "not_started"},
		{"encoding", "not_started"},
	}
	jobs := make([]model.Job, 0, len(statuses))
	for _, item := range statuses {
		progress := 0
		attempts := 0
		if item.status == "completed" {
			progress = 100
			attempts = 1
		}
		jobs = append(jobs, model.Job{
			UID: uuidv7.New(), MeetingUID: meetingUID, Stage: item.stage,
			Status: item.status, Attempts: attempts, Progress: progress,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	return jobs
}

func speakerNames(speakers []model.Speaker) map[string]string {
	names := make(map[string]string, len(speakers))
	for _, speaker := range speakers {
		names[speaker.ID] = speaker.DisplayName
	}
	return names
}

func splitMeetingSegments(segments []model.Segment, splitMS int64) ([]model.Segment, []model.Segment) {
	first := make([]model.Segment, 0, len(segments))
	second := make([]model.Segment, 0, len(segments))
	for _, segment := range segments {
		if segment.EndMS <= 0 || segment.StartMS >= segment.EndMS {
			continue
		}
		switch {
		case segment.EndMS <= splitMS:
			copy := segment
			copy.ID = uuidv7.New()
			first = append(first, copy)
		case segment.StartMS >= splitMS:
			copy := segment
			copy.ID = uuidv7.New()
			copy.StartMS -= splitMS
			copy.EndMS -= splitMS
			if copy.StartMS < 0 {
				copy.StartMS = 0
			}
			second = append(second, copy)
		default:
			// A phrase crossing the boundary is kept in both parts, clipped to
			// each part, so neither transcript loses the words.
			left := segment
			left.ID = uuidv7.New()
			left.EndMS = splitMS
			first = append(first, left)
			right := segment
			right.ID = uuidv7.New()
			right.StartMS = 0
			right.EndMS = segment.EndMS - splitMS
			second = append(second, right)
		}
	}
	sortSegments(first)
	sortSegments(second)
	return first, second
}

func sortSegments(segments []model.Segment) {
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].StartMS == segments[j].StartMS {
			return segments[i].EndMS < segments[j].EndMS
		}
		return segments[i].StartMS < segments[j].StartMS
	})
}

func (s *Server) splitMeetingAudio(original, first, second model.Meeting, splitMS int64, artifacts *[]model.Artifact) error {
	micSource := s.splitAudioSource(original, "mic_wav", "microphone.wav")
	systemSource := s.splitAudioSource(original, "system_wav", "system.wav")
	mixedSource := s.splitAudioSource(original, "mixed_wav", "mixed.wav")
	if mixedSource == "" {
		mixedSource = s.splitAudioSource(original, "imported_mixed_wav", "mixed.wav")
	}
	s.logger.Printf("meeting split audio uid=%s mic=%t system=%t mixed=%t", original.UID, micSource != "", systemSource != "", mixedSource != "")
	cleanup := make([]string, 0)
	defer func() {
		for _, path := range cleanup {
			_ = os.Remove(path)
		}
	}()
	// When a separate track is missing (a mixed-source meeting, or an archived
	// meeting with only mixed.opus), decode the mixed archive to fill the gap.
	// Otherwise the split would produce a silent system track and the remote
	// participants would disappear.
	if mixedSource == "" && (micSource == "" || systemSource == "") {
		if archive := s.splitArchiveSource(original); archive != "" {
			temp := filepath.Join(os.TempDir(), "lma-split-"+shortUID(original.UID)+".wav")
			if err := audio.NormalizeImportedAudio(archive, temp); err != nil {
				return fmt.Errorf("decode archived audio: %w", err)
			}
			mixedSource = temp
			cleanup = append(cleanup, temp)
		}
	}
	if micSource == "" && systemSource == "" && mixedSource == "" {
		return nil
	}
	for _, part := range []model.Meeting{first, second} {
		if err := s.splitPartAudio(original, part, splitMS, micSource, systemSource, mixedSource, artifacts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) splitAudioSource(meeting model.Meeting, artifactType, name string) string {
	artifacts, _ := s.store.Artifacts(meeting.UID)
	for _, artifact := range artifacts {
		if artifact.Type == artifactType && s.safeManagedPath(artifact.Path) && validWAVFile(artifact.Path) {
			return artifact.Path
		}
	}
	path := filepath.Join(s.meetingDir(meeting), "audio", name)
	if validWAVFile(path) {
		return path
	}
	return ""
}

func (s *Server) splitArchiveSource(meeting model.Meeting) string {
	artifacts, _ := s.store.Artifacts(meeting.UID)
	for _, artifact := range artifacts {
		if (artifact.Type == "mixed_opus" || artifact.Type == "mixed_mp3") && s.safeManagedPath(artifact.Path) {
			if info, err := os.Stat(artifact.Path); err == nil && info.Size() > 0 {
				return artifact.Path
			}
		}
	}
	return ""
}

func (s *Server) splitPartAudio(original, part model.Meeting, splitMS int64, micSource, systemSource, mixedSource string, artifacts *[]model.Artifact) error {
	audioDir := filepath.Join(s.meetingDir(part), "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		return err
	}
	start := int64(0)
	if part.StartedAt.After(original.StartedAt) {
		start = splitMS
	}
	duration := part.DurationMS
	micPath := filepath.Join(audioDir, "microphone.wav")
	systemPath := filepath.Join(audioDir, "system.wav")
	mixedPath := filepath.Join(audioDir, "mixed.wav")
	micInput := micSource
	systemInput := systemSource
	if micInput == "" && mixedSource != "" {
		micInput = mixedSource
	}
	if systemInput == "" && mixedSource != "" {
		systemInput = mixedSource
	}
	if micInput != "" || systemInput != "" {
		if err := writeSplitTrack(micInput, micPath, start, duration); err != nil {
			return fmt.Errorf("split microphone: %w", err)
		}
		if err := writeSplitTrack(systemInput, systemPath, start, duration); err != nil {
			return fmt.Errorf("split system audio: %w", err)
		}
		if mixedSource != "" {
			if err := writeSplitTrack(mixedSource, mixedPath, start, duration); err != nil {
				return fmt.Errorf("split mixed audio: %w", err)
			}
		} else {
			cfg := s.config()
			if err := audio.MixPCM16WAV(micPath, systemPath, mixedPath, cfg.Audio.MicrophoneGain, cfg.Audio.SystemGain); err != nil {
				return err
			}
		}
	} else {
		return nil
	}
	for _, item := range []struct{ kind, path string }{{"mic_wav", micPath}, {"system_wav", systemPath}, {"mixed_wav", mixedPath}} {
		record, err := artifactRecord(part.UID, item.kind, item.path, "audio/wav", "split")
		if err != nil {
			return err
		}
		*artifacts = append(*artifacts, record)
	}
	return nil
}

// writeSplitTrack writes exactly durationMS of PCM16 mono 16 kHz audio. The
// source range is copied when it exists; the rest is padded with silence.
func writeSplitTrack(sourcePath, targetPath string, startMS, durationMS int64) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return err
	}
	writer, err := audio.NewWAVWriter(targetPath, 16000, 1)
	if err != nil {
		return err
	}
	const frameBytes = 2
	wanted := durationMS * 16000 / 1000 * frameBytes
	var written int64
	if sourcePath != "" {
		if info, err := os.Stat(sourcePath); err == nil && info.Size() > 44 {
			source, err := os.Open(sourcePath)
			if err != nil {
				_ = writer.Close()
				return err
			}
			wav, err := audio.ReadWAVInfo(source)
			if err == nil && wav.SampleRate == 16000 && wav.Channels == 1 && wav.BitsPerSample == 16 {
				startByte := startMS * 16000 / 1000 * frameBytes
				endByte := startByte + wanted
				if startByte < wav.DataSize {
					if endByte > wav.DataSize {
						endByte = wav.DataSize
					}
					if endByte > startByte {
						if _, err := source.Seek(wav.DataOffset+startByte, io.SeekStart); err != nil {
							_ = source.Close()
							_ = writer.Close()
							return err
						}
						remaining := endByte - startByte
						buffer := make([]byte, 64*1024)
						for remaining > 0 {
							want := int64(len(buffer))
							if remaining < want {
								want = remaining
							}
							n, readErr := source.Read(buffer[:want])
							if n > 0 {
								if err := writer.WritePCM16(buffer[:n]); err != nil {
									_ = source.Close()
									_ = writer.Close()
									return err
								}
								written += int64(n)
							}
							if readErr != nil {
								break
							}
							remaining -= int64(n)
						}
					}
				}
			}
			_ = source.Close()
		}
	}
	if written < wanted {
		zeros := make([]byte, 64*1024)
		for written < wanted {
			want := int64(len(zeros))
			if wanted-written < want {
				want = wanted - written
			}
			if want%frameBytes != 0 {
				want -= want % frameBytes
			}
			if want <= 0 {
				break
			}
			if err := writer.WritePCM16(zeros[:want]); err != nil {
				_ = writer.Close()
				return err
			}
			written += want
		}
	}
	return writer.Close()
}

func (s *Server) writeSplitTranscript(original, part model.Meeting, segments []model.Segment, content string, artifacts *[]model.Artifact) error {
	dir := filepath.Join(s.meetingDir(part), "transcript")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "transcript.md")
	body := fmt.Sprintf("---\nmeeting_uid: %s\nartifact_type: transcript\ncreated_at: %s\nschema_version: 1\n---\n\n%s\n", part.UID, time.Now().UTC().Format(time.RFC3339Nano), content)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	record, err := artifactRecord(part.UID, "transcript", path, "text/markdown", "split")
	if err != nil {
		return err
	}
	*artifacts = append(*artifacts, record)
	return nil
}

func (s *Server) splitRawTranscripts(original, first, second model.Meeting, splitMS int64, artifacts *[]model.Artifact) error {
	originalArtifacts, err := s.store.Artifacts(original.UID)
	if err != nil {
		return err
	}
	for _, artifact := range originalArtifacts {
		switch artifact.Type {
		case "microphone_transcript_json", "system_transcript_json", "mixed_transcript_json":
		default:
			continue
		}
		raw, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			continue
		}
		var envelope struct {
			SchemaVersion int             `json:"schema_version"`
			ArtifactType  string          `json:"artifact_type"`
			Segments      []model.Segment `json:"segments"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		firstSegments, secondSegments := splitMeetingSegments(envelope.Segments, splitMS)
		kind := strings.TrimSuffix(artifact.Type, "_json")
		for _, item := range []struct {
			part     model.Meeting
			segments []model.Segment
		}{{first, firstSegments}, {second, secondSegments}} {
			dir := filepath.Join(s.meetingDir(item.part), "transcript")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			path := filepath.Join(dir, kind+".json")
			payload := map[string]any{"schema_version": 1, "meeting_uid": item.part.UID, "artifact_type": kind, "segments": item.segments}
			body, _ := json.MarshalIndent(payload, "", "  ")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return err
			}
			record, err := artifactRecord(item.part.UID, artifact.Type, path, "application/json", "split")
			if err != nil {
				return err
			}
			*artifacts = append(*artifacts, record)
		}
	}
	return nil
}

func (s *Server) splitDiarizationArtifact(original, first, second model.Meeting, splitMS int64, artifacts *[]model.Artifact) error {
	originalArtifacts, err := s.store.Artifacts(original.UID)
	if err != nil {
		return err
	}
	for _, artifact := range originalArtifacts {
		if artifact.Type != "diarization_json" {
			continue
		}
		raw, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			continue
		}
		var envelope diarizationArtifact
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		firstTurns, secondTurns := splitTurns(envelope.Turns, splitMS)
		firstCandidates, secondCandidates := splitCandidates(envelope.Candidates, splitMS)
		for _, item := range []struct {
			part       model.Meeting
			turns      []model.SpeakerTurn
			candidates []model.VoiceCandidate
		}{{first, firstTurns, firstCandidates}, {second, secondTurns, secondCandidates}} {
			dir := filepath.Join(s.meetingDir(item.part), "transcript")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			path := filepath.Join(dir, "diarization.json")
			payload := map[string]any{
				"schema_version":  3,
				"meeting_uid":     item.part.UID,
				"artifact_type":   "diarization",
				"audio_source":    envelope.AudioSource,
				"embedding_model": envelope.EmbeddingModel,
				"turns":           item.turns,
				"candidates":      item.candidates,
				"centroids":       envelope.Centroids,
			}
			body, _ := json.MarshalIndent(payload, "", "  ")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return err
			}
			record, err := artifactRecord(item.part.UID, "diarization_json", path, "application/json", "split")
			if err != nil {
				return err
			}
			*artifacts = append(*artifacts, record)
		}
	}
	return nil
}

func splitTurns(turns []model.SpeakerTurn, splitMS int64) ([]model.SpeakerTurn, []model.SpeakerTurn) {
	first := make([]model.SpeakerTurn, 0, len(turns))
	second := make([]model.SpeakerTurn, 0, len(turns))
	for _, turn := range turns {
		switch {
		case turn.EndMS <= splitMS:
			first = append(first, turn)
		case turn.StartMS >= splitMS:
			copy := turn
			copy.StartMS -= splitMS
			copy.EndMS -= splitMS
			second = append(second, copy)
		default:
			left := turn
			left.EndMS = splitMS
			first = append(first, left)
			right := turn
			right.StartMS = 0
			right.EndMS = turn.EndMS - splitMS
			second = append(second, right)
		}
	}
	return first, second
}

func splitCandidates(candidates []model.VoiceCandidate, splitMS int64) ([]model.VoiceCandidate, []model.VoiceCandidate) {
	first := make([]model.VoiceCandidate, 0, len(candidates))
	second := make([]model.VoiceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		switch {
		case candidate.EndMS <= splitMS:
			first = append(first, candidate)
		case candidate.StartMS >= splitMS:
			copy := candidate
			copy.StartMS -= splitMS
			copy.EndMS -= splitMS
			second = append(second, copy)
		default:
			left := candidate
			left.EndMS = splitMS
			first = append(first, left)
			right := candidate
			right.StartMS = 0
			right.EndMS = candidate.EndMS - splitMS
			second = append(second, right)
		}
	}
	return first, second
}

func (s *Server) copySplitAttachments(original, first, second model.Meeting, artifacts *[]model.Artifact) error {
	skip := map[string]bool{
		"mic_wav": true, "system_wav": true, "mixed_wav": true, "imported_mixed_wav": true, "mixed_opus": true, "mixed_mp3": true,
		"mic_clean_wav": true,
		"transcript":    true, "microphone_transcript_json": true, "system_transcript_json": true,
		"mixed_transcript_json": true, "diarization_json": true, "summary": true,
	}
	originalArtifacts, err := s.store.Artifacts(original.UID)
	if err != nil {
		return err
	}
	for _, artifact := range originalArtifacts {
		if skip[artifact.Type] || strings.HasPrefix(artifact.MIMEType, "audio/") || !s.safeManagedPath(artifact.Path) {
			continue
		}
		info, statErr := os.Stat(artifact.Path)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		for _, part := range []model.Meeting{first, second} {
			dir := filepath.Join(s.meetingDir(part), "sources")
			destination := filepath.Join(dir, shortUID(artifact.UID)+"-"+sanitizeName(filepath.Base(artifact.Path)))
			if err := copyFile(artifact.Path, destination); err != nil {
				return err
			}
			record, err := artifactRecord(part.UID, "source_"+artifact.Type, destination, artifact.MIMEType, "split_source")
			if err != nil {
				return err
			}
			*artifacts = append(*artifacts, record)
		}
	}
	return nil
}
