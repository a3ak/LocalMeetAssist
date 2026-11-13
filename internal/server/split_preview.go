package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/model"
)

// splitPreviewAudio returns a single playable track for the split dialog. When
// the meeting has no mixed file, microphone and system WAVs are mixed on demand
// and padded with silence to the meeting duration, so the timeline shows where
// the meeting actually ended instead of stopping at a shorter system stream.
func (s *Server) splitPreviewAudio(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	meeting, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found")
		return
	}
	if meeting.Status != "completed" && meeting.Status != "warning" {
		writeError(w, http.StatusConflict, "only completed meetings can be previewed for splitting")
		return
	}
	s.splitPreviewMu.Lock()
	defer s.splitPreviewMu.Unlock()
	if path, artifact := s.splitMixedSource(meeting); path != "" {
		if artifact.UID == "" {
			artifact, err = s.makeArtifact(meeting.UID, "mixed_wav", path, "audio/wav", "generated")
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifact": artifact})
		return
	}
	micSource := s.splitAudioSource(meeting, "mic_wav", "microphone.wav")
	if micSource == "" {
		micSource = s.splitAudioSource(meeting, "mic_clean_wav", "microphone_clean.wav")
	}
	systemSource := s.splitAudioSource(meeting, "system_wav", "system.wav")
	if micSource == "" && systemSource == "" {
		writeError(w, http.StatusNotFound, "meeting has no audio to preview")
		return
	}
	audioDir := filepath.Join(s.meetingDir(meeting), "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cleanup := make([]string, 0)
	defer func() {
		for _, path := range cleanup {
			_ = os.Remove(path)
		}
	}()
	micPath, systemPath := micSource, systemSource
	if micPath == "" {
		micPath = filepath.Join(audioDir, ".preview-microphone.wav")
		if err := audio.CreateSilentPCM16WAVLike(systemPath, micPath); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cleanup = append(cleanup, micPath)
	}
	if systemPath == "" {
		systemPath = filepath.Join(audioDir, ".preview-system.wav")
		if err := audio.CreateSilentPCM16WAVLike(micPath, systemPath); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cleanup = append(cleanup, systemPath)
	}
	mixedPath := filepath.Join(audioDir, "mixed.wav")
	cfg := s.config()
	if err := audio.MixPCM16WAV(micPath, systemPath, mixedPath, cfg.Audio.MicrophoneGain, cfg.Audio.SystemGain); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mix preview audio: %v", err))
		return
	}
	duration := meeting.DurationMS
	if duration == 0 {
		duration = s.splitMeetingDuration(meeting)
	}
	if mixedDuration, err := audio.PCM16WAVDurationMS(mixedPath); err == nil && duration > mixedDuration+1000 {
		padded := mixedPath + ".preview-pad"
		if err := writeSplitTrack(mixedPath, padded, 0, duration); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("pad preview audio: %v", err))
			return
		}
		if err := os.Rename(padded, mixedPath); err != nil {
			_ = os.Remove(padded)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("replace padded preview: %v", err))
			return
		}
	}
	artifact, err := s.makeArtifact(meeting.UID, "mixed_wav", mixedPath, "audio/wav", "generated")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Printf("split preview mixed audio uid=%s mic=%t system=%t duration_ms=%d", meeting.UID, micSource != "", systemSource != "", duration)
	writeJSON(w, http.StatusOK, map[string]any{"artifact": artifact})
}

func (s *Server) splitMixedSource(meeting model.Meeting) (string, model.Artifact) {
	artifacts, _ := s.store.Artifacts(meeting.UID)
	for _, kind := range []string{"mixed_wav", "imported_mixed_wav", "mixed_opus", "mixed_mp3"} {
		for _, artifact := range artifacts {
			if artifact.Type != kind || !s.safeManagedPath(artifact.Path) {
				continue
			}
			if info, err := os.Stat(artifact.Path); err == nil && info.Size() > 0 {
				return artifact.Path, artifact
			}
		}
	}
	path := filepath.Join(s.meetingDir(meeting), "audio", "mixed.wav")
	if validWAVFile(path) {
		artifact, _ := s.makeArtifact(meeting.UID, "mixed_wav", path, "audio/wav", "generated")
		return path, artifact
	}
	return "", model.Artifact{}
}
