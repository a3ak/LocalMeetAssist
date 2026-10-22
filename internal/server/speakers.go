package server

import (
	"context"
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
	// Create adds speakers that diarization did not find, for example when two
	// participants were detected as one voice. Fragments are moved into them in
	// the same request, so a new card is never created empty by accident.
	Create []speakerCreate `json:"create"`
	// Voice memory actions. PersonIDs explicitly links a speaker to a known
	// profile (clicked suggestion or namesake choice), Confirm accepts the
	// automatic name and enrolls the voice, Reject removes an automatic match.
	PersonIDs map[string]string `json:"person_ids"`
	Confirm   []string          `json:"confirm"`
	Reject    []string          `json:"reject"`
}

// speakerCreate is one speaker to add to a meeting.
type speakerCreate struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Source      string `json:"source"`
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
	if len(req.Names)+len(req.Assignments)+len(req.Merges)+len(req.PersonIDs)+len(req.Confirm)+len(req.Reject) > 10000 {
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
	// Источники встречи: новый спикер живёт в том же потоке, что и фрагменты,
	// которые в него переносят, иначе перенос был бы запрещён.
	sources := make(map[string]bool, 2)
	for _, speaker := range existing {
		if speaker.Source != "" {
			sources[speaker.Source] = true
		}
	}
	if len(sources) == 0 {
		sources["system"] = true
		sources["microphone"] = true
	}
	defaultSource := "system"
	if !sources["system"] {
		for source := range sources {
			defaultSource = source
		}
	}
	created := make([]string, 0, len(req.Create))
	for _, item := range req.Create {
		if len(created) >= 50 {
			writeError(w, http.StatusBadRequest, "too many new speakers")
			return
		}
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 64 || strings.ContainsAny(id, "/\\: \t\n") {
			writeError(w, http.StatusBadRequest, "invalid new speaker id")
			return
		}
		if _, exists := byID[id]; exists {
			writeError(w, http.StatusBadRequest, "speaker already exists: "+id)
			return
		}
		source := strings.TrimSpace(item.Source)
		if source == "" {
			source = defaultSource
		}
		if !sources[source] {
			writeError(w, http.StatusBadRequest, "unknown speaker source: "+source)
			return
		}
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = id
		}
		byID[id] = model.Speaker{MeetingUID: uid, ID: id, DisplayName: name, Source: source}
		created = append(created, id)
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
	// Voice memory actions are applied before the cards are rebuilt, so the
	// rebuilt speaker keeps the chosen link instead of the old automatic one.
	for id := range req.PersonIDs {
		if _, ok := byID[id]; !ok {
			writeError(w, http.StatusBadRequest, "unknown speaker: "+id)
			return
		}
	}
	for _, id := range req.Confirm {
		if _, ok := byID[id]; !ok {
			writeError(w, http.StatusBadRequest, "unknown speaker: "+id)
			return
		}
	}
	for _, id := range req.Reject {
		speaker, ok := byID[id]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown speaker: "+id)
			return
		}
		speaker.PersonID = ""
		speaker.MatchScore = 0
		speaker.MatchCandidates = nil
		speaker.NameSource = ""
		if _, named := req.Names[id]; !named {
			speaker.DisplayName = speaker.ID
		}
		byID[id] = speaker
	}
	for id, personID := range req.PersonIDs {
		person, err := s.store.Person(personID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unknown person for speaker: "+id)
			return
		}
		speaker := byID[id]
		speaker.PersonID = person.ID
		speaker.NameSource = "manual"
		speaker.MatchCandidates = nil
		if strings.TrimSpace(speaker.DisplayName) == "" || speaker.DisplayName == id || strings.HasPrefix(speaker.DisplayName, "SPEAKER_") {
			speaker.DisplayName = person.Name
		}
		byID[id] = speaker
	}
	for _, id := range req.Confirm {
		speaker := byID[id]
		speaker.NameSource = "manual"
		speaker.MatchCandidates = nil
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
	// Новый спикер без фрагментов всё равно должен попасть в список: иначе
	// карточка исчезнет сразу после сохранения.
	present := make(map[string]bool, len(speakers))
	for _, speaker := range speakers {
		present[speaker.ID] = true
	}
	for _, id := range created {
		if !present[id] {
			speakers = append(speakers, byID[id])
		}
	}
	sort.Slice(speakers, func(i, j int) bool { return speakers[i].ID < speakers[j].ID })
	confirm := make(map[string]bool, len(req.Confirm))
	for _, id := range req.Confirm {
		confirm[id] = true
	}
	manual := make(map[string]bool, len(req.Names))
	previousPersonIDs := make(map[string]string, len(req.Names))
	for index := range speakers {
		speaker := &speakers[index]
		if speaker.ID == "microphone_owner" || speaker.Source == "microphone" {
			continue
		}
		name := strings.TrimSpace(speaker.DisplayName)
		if name == "" || name == speaker.ID || strings.HasPrefix(name, "SPEAKER_") {
			continue
		}
		_, named := req.Names[speaker.ID]
		changed := named && name != strings.TrimSpace(oldNames[speaker.ID])
		if changed || confirm[speaker.ID] || req.PersonIDs[speaker.ID] != "" {
			manual[speaker.ID] = true
			speaker.NameSource = "manual"
			speaker.MatchCandidates = nil
		}
		// A changed name is a correction, not necessarily a different person.
		// Keep the previous automatic link for enrollment: if the voice matches,
		// the profile is renamed; if not, enrollment resolves another person.
		if changed && !confirm[speaker.ID] && req.PersonIDs[speaker.ID] == "" {
			if speaker.PersonID != "" {
				previousPersonIDs[speaker.ID] = speaker.PersonID
			}
			speaker.PersonID = ""
			speaker.MatchScore = 0
		}
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
	var enrollment voiceEnrollmentResult
	if len(manual) > 0 && s.config().Diarization.RememberSpeakers {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		enrollment = s.enrollSpeakers(ctx, meeting, segments, speakers, manual, req.PersonIDs, previousPersonIDs)
		cancel()
		for index := range speakers {
			if enrollment.Updated[speakers[index].ID] {
				speakers[index] = enrollment.Speakers[speakers[index].ID]
			}
		}
	}
	s.logger.Printf("speaker edits saved meeting_uid=%s names=%d assignments=%d merges=%d created=%d voice_added=%d", uid, len(req.Names), len(req.Assignments), len(req.Merges), len(created), enrollment.Added)
	response := map[string]any{"saved": true, "speakers": speakers, "transcript": meeting.Transcript, "summary_stale": meeting.Metadata["summary_stale"] == "true"}
	if len(manual) > 0 {
		response["voice_enrollment"] = enrollment.Summary
	}
	writeJSON(w, http.StatusOK, response)
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
			old, known := previous[segment.SpeakerID]
			if known && strings.TrimSpace(old.DisplayName) != "" {
				displayName = old.DisplayName
			} else if segment.SpeakerID == "microphone_owner" {
				displayName = defaultOwnerName
			}
			speaker = &model.Speaker{MeetingUID: meetingUID, ID: segment.SpeakerID, DisplayName: displayName, Source: segment.Source}
			if known {
				// Voice links are not derivable from segments; they must survive
				// every rebuild of the speaker cards.
				speaker.PersonID = old.PersonID
				speaker.MatchScore = old.MatchScore
				speaker.MatchCandidates = old.MatchCandidates
				speaker.NameSource = old.NameSource
			}
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
