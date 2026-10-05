package server

import (
	"errors"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

var audioImportTypes = map[string]string{
	"audio_microphone": "microphone",
	"audio_system":     "system",
	"audio_mixed":      "mixed",
}

// importAudio normalizes an uploaded file into the canonical audio/ pair.
func (s *Server) importAudio(meeting model.Meeting, file multipart.File, kind string) (model.Artifact, error) {
	importType, ok := audioImportTypes[kind]
	if !ok {
		return model.Artifact{}, errors.New("unknown audio import type")
	}
	if meeting.Status == "recording" || meeting.Status == "processing" || s.pipeline.IsRunning(meeting.UID) {
		return model.Artifact{}, errors.New("audio cannot be replaced while recording or processing")
	}
	audioDir := filepath.Join(s.meetingDir(meeting), "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		return model.Artifact{}, err
	}
	uploadPath := filepath.Join(audioDir, ".import-"+uuidv7.New()+".upload")
	normalizedPath := filepath.Join(audioDir, ".normalized-"+uuidv7.New()+".wav")
	silencePath := filepath.Join(audioDir, ".silence-"+uuidv7.New()+".wav")
	defer os.Remove(uploadPath)
	defer os.Remove(normalizedPath)
	defer os.Remove(silencePath)
	if err := saveMultipart(file, uploadPath); err != nil {
		return model.Artifact{}, err
	}
	if err := audio.NormalizeImportedAudio(uploadPath, normalizedPath); err != nil {
		return model.Artifact{}, fmt.Errorf("could not prepare audio: %w", err)
	}
	info, err := validateImportedWAV(normalizedPath)
	if err != nil {
		return model.Artifact{}, err
	}
	if err := audio.CreateSilentPCM16WAVLike(normalizedPath, silencePath); err != nil {
		return model.Artifact{}, err
	}

	micPath := filepath.Join(audioDir, "microphone.wav")
	systemPath := filepath.Join(audioDir, "system.wav")
	sources := importedAudioSources(meeting.Metadata)
	// Legacy native recordings have no import metadata. When their canonical
	// WAV files still exist, importing one replacement must preserve the other.
	if len(sources) == 0 && meeting.Source != "imported" {
		if validWAVFile(micPath) {
			sources["microphone"] = true
		}
		if validWAVFile(systemPath) {
			sources["system"] = true
		}
	}
	// A previous Opus/delete policy may have removed canonical WAV files while
	// leaving import metadata. Never mistake a missing file for a real source.
	if !validWAVFile(micPath) {
		delete(sources, "microphone")
	}
	if !validWAVFile(systemPath) {
		delete(sources, "system")
	}

	if importType == "mixed" {
		// Mixed is an exclusive source: it is stored in system.wav with a silent
		// microphone companion so the common mixer/encoder path remains usable.
		sources = map[string]bool{"mixed": true}
		if err := replaceImportedPair(normalizedPath, systemPath, silencePath, micPath); err != nil {
			return model.Artifact{}, err
		}
	} else {
		// Switching away from a mixed import starts a new pair of independent
		// sources. Subsequent microphone/system imports accumulate instead of
		// replacing one another.
		if sources["mixed"] {
			sources = make(map[string]bool)
		}
		sources[importType] = true
		targetPath, companionPath, companionType := micPath, systemPath, "system"
		if importType == "system" {
			targetPath, companionPath, companionType = systemPath, micPath, "microphone"
		}
		if sources[companionType] && validWAVFile(companionPath) {
			if err := replaceImportedFile(normalizedPath, targetPath); err != nil {
				return model.Artifact{}, err
			}
		} else if err := replaceImportedPair(normalizedPath, targetPath, silencePath, companionPath); err != nil {
			return model.Artifact{}, err
		}
	}

	// A changed source invalidates generated outputs. Recreate canonical audio
	// records below, but preserve the other real source file in a separate pair.
	artifacts, _ := s.store.Artifacts(meeting.UID)
	// Both canonical paths are required by retry and mixing. One of them may be
	// an intentionally silent companion and must not be removed while stale
	// artifact records are being cleaned.
	preservedPaths := map[string]bool{
		filepath.Clean(micPath):    true,
		filepath.Clean(systemPath): true,
	}
	for _, artifact := range artifacts {
		canonicalAudio := artifact.Type == "mic_wav" || artifact.Type == "system_wav" || artifact.Type == "imported_mixed_wav" || artifact.Type == "mixed_wav" || artifact.Type == "mixed_opus" || artifact.Type == "mixed_mp3"
		if artifact.Source != "generated" && !canonicalAudio {
			continue
		}
		_, _ = s.store.DeleteArtifact(artifact.UID)
		if !preservedPaths[filepath.Clean(artifact.Path)] && s.safeManagedPath(artifact.Path) {
			_ = os.Remove(artifact.Path)
		}
	}
	_ = s.store.DeleteSpeakers(meeting.UID)
	if meeting.Metadata == nil {
		meeting.Metadata = make(map[string]string)
	}
	meeting.Metadata["audio_import_sources"] = formatImportedAudioSources(sources)
	if sources["mixed"] {
		meeting.Metadata["audio_import_type"] = "mixed"
		meeting.Metadata["transcription_source_mode"] = "mixed"
	} else {
		if sources["microphone"] && sources["system"] {
			meeting.Metadata["audio_import_type"] = "separate"
			// With two real inputs mixed.wav is materially different and is a
			// useful fallback if the direct system transcription degenerates.
			meeting.Metadata["transcription_source_mode"] = "auto"
		} else {
			meeting.Metadata["audio_import_type"] = importType
			// A single source plus silence would produce an identical mixed file,
			// so a second Whisper pass cannot improve the result.
			meeting.Metadata["transcription_source_mode"] = "separate"
		}
	}
	meeting.Source = "imported"
	meeting.Status = "created"
	meeting.ProcessingStage = ""
	meeting.LastError = ""
	meeting.Transcript = ""
	meeting.Summary = ""
	meeting.SummaryStatus = "pending"
	meeting.SpeakerCount = 0
	meeting.UpdatedAt = time.Now()
	if err := s.store.SaveMeeting(meeting); err != nil {
		return model.Artifact{}, err
	}

	var importedArtifact model.Artifact
	for _, source := range []string{"microphone", "system", "mixed"} {
		if !sources[source] {
			continue
		}
		artifactType := map[string]string{"microphone": "mic_wav", "system": "system_wav", "mixed": "imported_mixed_wav"}[source]
		artifactPath := micPath
		if source == "system" || source == "mixed" {
			artifactPath = systemPath
		}
		artifact, artifactErr := s.makeArtifact(meeting.UID, artifactType, artifactPath, "audio/wav", "imported")
		if artifactErr != nil {
			return model.Artifact{}, artifactErr
		}
		if source == importType {
			importedArtifact = artifact
		}
	}
	s.logger.Printf("audio imported uid=%s type=%s sources=%q sample_rate=%d channels=%d bytes=%d", meeting.UID, importType, meeting.Metadata["audio_import_sources"], info.SampleRate, info.Channels, info.DataSize)
	return importedArtifact, nil
}

func importedAudioSources(metadata map[string]string) map[string]bool {
	sources := make(map[string]bool)
	for _, value := range strings.Split(metadata["audio_import_sources"], ",") {
		switch value = strings.ToLower(strings.TrimSpace(value)); value {
		case "microphone", "system", "mixed":
			sources[value] = true
		}
	}
	if len(sources) == 0 {
		switch legacy := strings.ToLower(strings.TrimSpace(metadata["audio_import_type"])); legacy {
		case "microphone", "system", "mixed":
			sources[legacy] = true
		}
	}
	return sources
}

func formatImportedAudioSources(sources map[string]bool) string {
	values := make([]string, 0, 2)
	for _, source := range []string{"microphone", "system", "mixed"} {
		if sources[source] {
			values = append(values, source)
		}
	}
	return strings.Join(values, ",")
}

func validateImportedWAV(path string) (audio.WAVInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return audio.WAVInfo{}, err
	}
	info, err := audio.ReadWAVInfo(file)
	_ = file.Close()
	if err != nil {
		return audio.WAVInfo{}, fmt.Errorf("unsupported audio file: %w", err)
	}
	if info.BitsPerSample != 16 || (info.Channels != 1 && info.Channels != 2) {
		return audio.WAVInfo{}, fmt.Errorf("PCM16 WAV mono/stereo required; got %d bit, %d channel(s)", info.BitsPerSample, info.Channels)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return audio.WAVInfo{}, err
	}
	if info.DataSize <= 0 || info.DataOffset+info.DataSize > stat.Size() {
		return audio.WAVInfo{}, errors.New("WAV is empty or truncated")
	}
	return info, nil
}

func replaceImportedPair(firstSource, firstTarget, secondSource, secondTarget string) error {
	targets := []string{firstTarget, secondTarget}
	sources := []string{firstSource, secondSource}
	backups := []string{firstTarget + ".before-import", secondTarget + ".before-import"}
	for _, backup := range backups {
		_ = os.Remove(backup)
	}
	var backedUp []int
	for index, target := range targets {
		if _, err := os.Stat(target); err == nil {
			if err := os.Rename(target, backups[index]); err != nil {
				for _, restore := range backedUp {
					_ = os.Rename(backups[restore], targets[restore])
				}
				return err
			}
			backedUp = append(backedUp, index)
		}
	}
	committed := 0
	for index := range sources {
		if err := os.Rename(sources[index], targets[index]); err != nil {
			for restore := 0; restore < committed; restore++ {
				_ = os.Remove(targets[restore])
			}
			for restore := range backups {
				_ = os.Rename(backups[restore], targets[restore])
			}
			return err
		}
		committed++
	}
	for _, backup := range backups {
		_ = os.Remove(backup)
	}
	return nil
}

func replaceImportedFile(source, target string) error {
	backup := target + ".before-import"
	_ = os.Remove(backup)
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
		hadTarget = true
	}
	if err := os.Rename(source, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return err
	}
	_ = os.Remove(backup)
	return nil
}
