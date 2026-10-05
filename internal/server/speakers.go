package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/atomicfile"
	"localmeetassist/internal/model"
	"localmeetassist/internal/transcript"
)

// speakers lists the speakers of a meeting.
func (s *Server) speakers(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method == http.MethodPatch {
		s.updateSpeakers(w, r, uid)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	items, err := s.store.Speakers(uid)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, items)
}

// speakerBatchRequest is the body of a batch speaker edit.
type speakerBatchRequest struct {
	Names       map[string]string `json:"names"`
	Assignments map[string]string `json:"assignments"`
	Merges      map[string]string `json:"merges"`
}

// updateSpeakers applies names, fragment moves and merges atomically.
func (s *Server) updateSpeakers(w http.ResponseWriter, r *http.Request, uid string) {
	if s.pipeline.IsRunning(uid) {
		writeError(w, http.StatusConflict, "speakers cannot be changed while processing")
		return
	}
	var req speakerBatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Names)+len(req.Assignments)+len(req.Merges) > 10000 {
		writeError(w, http.StatusBadRequest, "too many changes")
		return
	}
	meeting, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found")
		return
	}
	existing, err := s.store.Speakers(uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byID := make(map[string]model.Speaker, len(existing))
	oldNames := make(map[string]string, len(existing))
	for _, speaker := range existing {
		byID[speaker.ID] = speaker
		oldNames[speaker.ID] = speaker.DisplayName
	}
	resolveMerge := func(id string) (string, error) {
		seen := make(map[string]bool)
		for {
			next := strings.TrimSpace(req.Merges[id])
			if next == "" {
				return id, nil
			}
			if seen[id] || next == id {
				return "", errors.New("speaker merge cycle detected")
			}
			seen[id] = true
			id = next
		}
	}
	for from, into := range req.Merges {
		left, leftOK := byID[from]
		right, rightOK := byID[into]
		if !leftOK || !rightOK {
			writeError(w, http.StatusBadRequest, "unknown speaker in the merge operation")
			return
		}
		if left.Source != right.Source {
			writeError(w, http.StatusBadRequest, "microphone and system streams cannot be merged")
			return
		}
		if _, err := resolveMerge(from); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	segments, segmentErr := s.store.TranscriptSegments(uid)
	if segmentErr != nil && (len(req.Assignments) > 0 || len(req.Merges) > 0) {
		writeError(w, http.StatusConflict, "to move fragments, re-run the diarization stage in the current LocalMeetAssist version")
		return
	}
	if segmentErr != nil {
		segments = nil
	}
	segmentIndex := make(map[string]int, len(segments))
	for index := range segments {
		segmentIndex[segments[index].ID] = index
		if target, err := resolveMerge(segments[index].SpeakerID); err == nil {
			segments[index].SpeakerID = target
		}
	}
	for fragmentID, targetID := range req.Assignments {
		index, ok := segmentIndex[fragmentID]
		target, targetOK := byID[targetID]
		if !ok || !targetOK {
			writeError(w, http.StatusBadRequest, "unknown fragment or target speaker")
			return
		}
		if source := byID[segments[index].SpeakerID]; source.Source != "" && source.Source != target.Source {
			writeError(w, http.StatusBadRequest, "fragments cannot be moved between microphone and system streams")
			return
		}
		segments[index].SpeakerID = targetID
	}
	for id, name := range req.Names {
		speaker, ok := byID[id]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown speaker: "+id)
			return
		}
		name = strings.TrimSpace(name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "the speaker name cannot be empty")
			return
		}
		speaker.DisplayName = name
		byID[id] = speaker
	}

	var speakers []model.Speaker
	if len(segments) > 0 {
		speakers = buildSpeakersFromSegments(uid, segments, byID, s.ownerName())
	} else {
		for _, speaker := range byID {
			speakers = append(speakers, speaker)
		}
		sort.Slice(speakers, func(i, j int) bool { return speakers[i].ID < speakers[j].ID })
	}
	names := make(map[string]string, len(speakers))
	for _, speaker := range speakers {
		names[speaker.ID] = speaker.DisplayName
	}
	if len(segments) > 0 {
		meeting.Transcript = formatMeetingTranscript(segments, names)
	} else {
		for id, oldName := range oldNames {
			meeting.Transcript = replaceTranscriptSpeakerName(meeting.Transcript, oldName, names[id])
		}
	}
	if meeting.Metadata == nil {
		meeting.Metadata = make(map[string]string)
	}
	if strings.TrimSpace(meeting.Summary) != "" {
		meeting.Metadata["summary_stale"] = "true"
	}
	meeting.SpeakerCount = len(speakers)
	meeting.UpdatedAt = time.Now()
	if err := s.rewriteTranscriptArtifact(uid, meeting.Transcript); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.ReplaceSpeakerState(meeting, segments, speakers); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Printf("speaker edits saved meeting_uid=%s names=%d assignments=%d merges=%d", uid, len(req.Names), len(req.Assignments), len(req.Merges))
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "speakers": speakers, "transcript": meeting.Transcript, "summary_stale": meeting.Metadata["summary_stale"] == "true"})
}

// speaker renames a single speaker.
func (s *Server) speaker(w http.ResponseWriter, r *http.Request, uid, id string) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	sp, err := s.store.Speaker(uid, id)
	if err != nil {
		writeError(w, 404, "speaker not found")
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, 400, "display_name is required")
		return
	}
	oldName := sp.DisplayName
	sp.DisplayName = strings.TrimSpace(req.DisplayName)
	if err := s.store.SaveSpeaker(sp); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if oldName != sp.DisplayName || sp.ID != sp.DisplayName {
		if err := s.renameSpeakerInTranscript(uid, sp.DisplayName, oldName, sp.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "the speaker name was saved but the transcript was not updated: "+err.Error())
			return
		}
	}
	writeJSON(w, 200, sp)
}

// renameSpeakerInTranscript rewrites the stored transcript artifact.
func (s *Server) renameSpeakerInTranscript(uid, newName string, oldNames ...string) error {
	meeting, err := s.store.Meeting(uid)
	if err != nil {
		return err
	}
	for _, oldName := range oldNames {
		meeting.Transcript = replaceTranscriptSpeakerName(meeting.Transcript, oldName, newName)
	}
	meeting.UpdatedAt = time.Now()
	if err := s.store.SaveMeeting(meeting); err != nil {
		return err
	}
	artifacts, err := s.store.Artifacts(uid)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.Type != "transcript" || !s.safeManagedPath(artifact.Path) {
			continue
		}
		data, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			return readErr
		}
		updated := string(data)
		for _, oldName := range oldNames {
			updated = replaceTranscriptSpeakerName(updated, oldName, newName)
		}
		if updated == string(data) {
			continue
		}
		tmp := artifact.Path + ".rename.tmp"
		if writeErr := os.WriteFile(tmp, []byte(updated), 0o600); writeErr != nil {
			return writeErr
		}
		if renameErr := os.Rename(tmp, artifact.Path); renameErr != nil {
			_ = os.Remove(tmp)
			return renameErr
		}
		hash := sha256.Sum256([]byte(updated))
		artifact.SizeBytes = int64(len(updated))
		artifact.SHA256 = hex.EncodeToString(hash[:])
		if saveErr := s.store.SaveArtifact(artifact); saveErr != nil {
			return saveErr
		}
	}
	return nil
}

// replaceTranscriptSpeakerName replaces a speaker label in transcript lines.
func replaceTranscriptSpeakerName(text, oldName, newName string) string {
	if text == "" || oldName == "" || oldName == newName {
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		closing := strings.Index(line, "] ")
		if closing < 0 {
			continue
		}
		prefixEnd := closing + 2
		if strings.HasPrefix(line[prefixEnd:], oldName+":") {
			lines[index] = line[:prefixEnd] + newName + line[prefixEnd+len(oldName):]
		}
	}
	return strings.Join(lines, "\n")
}

// buildSpeakersFromSegments rebuilds speaker cards from edited segments.
func buildSpeakersFromSegments(meetingUID string, segments []model.Segment, previous map[string]model.Speaker, defaultOwnerName string) []model.Speaker {
	grouped := make(map[string]*model.Speaker)
	for _, segment := range segments {
		if segment.SpeakerID == "" {
			continue
		}
		speaker := grouped[segment.SpeakerID]
		if speaker == nil {
			displayName := segment.SpeakerID
			if old, ok := previous[segment.SpeakerID]; ok && strings.TrimSpace(old.DisplayName) != "" {
				displayName = old.DisplayName
			} else if segment.SpeakerID == "microphone_owner" {
				displayName = defaultOwnerName
			}
			speaker = &model.Speaker{MeetingUID: meetingUID, ID: segment.SpeakerID, DisplayName: displayName, Source: segment.Source}
			grouped[segment.SpeakerID] = speaker
		}
		fragment := model.Sample{ID: segment.ID, StartMS: segment.StartMS, EndMS: segment.EndMS, Source: segment.Source, Text: segment.Text}
		speaker.Fragments = append(speaker.Fragments, fragment)
	}
	result := make([]model.Speaker, 0, len(grouped))
	for _, speaker := range grouped {
		sort.SliceStable(speaker.Fragments, func(i, j int) bool { return speaker.Fragments[i].StartMS < speaker.Fragments[j].StartMS })
		candidates := append([]model.Sample(nil), speaker.Fragments...)
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].EndMS-candidates[i].StartMS > candidates[j].EndMS-candidates[j].StartMS
		})
		for _, sample := range candidates {
			if sample.EndMS-sample.StartMS < 500 {
				continue
			}
			if sample.EndMS > sample.StartMS+10000 {
				sample.EndMS = sample.StartMS + 10000
			}
			speaker.Samples = append(speaker.Samples, sample)
			if len(speaker.Samples) == 3 {
				break
			}
		}
		result = append(result, *speaker)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// formatMeetingTranscript sorts segments and renders the Markdown transcript.
func formatMeetingTranscript(segments []model.Segment, names map[string]string) string {
	ordered := append([]model.Segment(nil), segments...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].StartMS == ordered[j].StartMS {
			return ordered[i].EndMS < ordered[j].EndMS
		}
		return ordered[i].StartMS < ordered[j].StartMS
	})
	return transcript.Format(ordered, names)
}

// rewriteTranscriptArtifact writes the transcript file and updates its record.
func (s *Server) rewriteTranscriptArtifact(meetingUID, transcript string) error {
	artifacts, err := s.store.Artifacts(meetingUID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.Type != "transcript" || !s.safeManagedPath(artifact.Path) {
			continue
		}
		body := fmt.Sprintf("---\nmeeting_uid: %s\nartifact_type: transcript\ncreated_at: %s\nschema_version: 1\n---\n\n%s\n", meetingUID, time.Now().UTC().Format(time.RFC3339Nano), transcript)
		if err := atomicfile.Write(artifact.Path, []byte(body), 0o600); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(body))
		artifact.SizeBytes = int64(len(body))
		artifact.SHA256 = hex.EncodeToString(hash[:])
		artifact.CreatedAt = time.Now()
		if err := s.store.SaveArtifact(artifact); err != nil {
			return err
		}
	}
	return nil
}

// mergeSpeakers merges one speaker into another in a single operation.
func (s *Server) mergeSpeakers(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		From string `json:"from"`
		Into string `json:"into"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.From == req.Into || req.From == "" || req.Into == "" {
		writeError(w, 400, "from and into must be different")
		return
	}
	if _, err := s.store.Speaker(uid, req.Into); err != nil {
		writeError(w, 404, "target speaker not found")
		return
	}
	if err := s.store.DeleteSpeaker(uid, req.From); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "merged"})
}
