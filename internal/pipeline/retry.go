package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"localmeetassist/internal/atomicfile"
	"localmeetassist/internal/audio"
	"localmeetassist/internal/model"
	"localmeetassist/internal/transcript"
)

// loadSegments loads the raw segments stored for one transcript artifact.
func (r *Runner) loadSegments(meetingUID, artifactType string) ([]model.Segment, error) {
	artifacts, err := r.store.Artifacts(meetingUID)
	if err != nil {
		return nil, err
	}
	var path string
	for _, artifact := range artifacts {
		if artifact.Type == artifactType {
			path = artifact.Path
		}
	}
	if path == "" {
		return nil, fmt.Errorf("artifact %s not found; re-run transcription first", artifactType)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document struct {
		Segments []model.Segment `json:"segments"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document.Segments, nil
}

// retryDiarization re-runs only the diarization stage on stored transcripts.
func (r *Runner) retryDiarization(ctx context.Context, uid string) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "processing", "diarizing", "", time.Now()
	_ = r.store.SaveMeeting(m)
	r.job(uid, "diarizing", "processing", "")

	micSegments, _ := r.loadSegments(uid, "microphone_transcript_json")
	systemSegments, systemErr := r.loadSegments(uid, "system_transcript_json")
	mixedSegments, mixedErr := r.loadSegments(uid, "mixed_transcript_json")
	sourceMode := strings.ToLower(strings.TrimSpace(r.cfg.Transcription.SourceMode))
	if override := strings.ToLower(strings.TrimSpace(m.Metadata["transcription_source_mode"])); override != "" {
		sourceMode = override
	}
	useMixed := mixedErr == nil && len(mixedSegments) > 0 && (sourceMode == "mixed" || systemErr != nil || len(systemSegments) == 0 || transcriptLooksDegenerate(systemSegments))
	if !useMixed && (systemErr != nil || len(systemSegments) == 0) {
		return r.fail(&m, "diarizing", errors.New("system transcript not found; re-run transcription first"))
	}

	dir := r.meetingDir(m)
	micPath := filepath.Join(dir, "audio", "microphone.wav")
	sysPath := filepath.Join(dir, "audio", "system.wav")
	if _, err := r.ensureCanonicalAudio(&m, micPath, sysPath); err != nil {
		return r.fail(&m, "diarizing", err)
	}
	audioPath := filepath.Join(dir, "audio", "system.wav")
	diarizationSegments := systemSegments
	if useMixed {
		audioPath = filepath.Join(dir, "audio", r.name(m, "mixed")+".wav")
		diarizationSegments = mixedSegments
		if !validCanonicalWAV(audioPath) {
			if err := audio.MixPCM16WAV(micPath, sysPath, audioPath, r.cfg.Audio.MicrophoneGain, r.cfg.Audio.SystemGain); err != nil {
				return r.fail(&m, "diarizing", err)
			}
			_, _ = r.addArtifact(uid, "mixed_wav", audioPath, "audio/wav", "restored")
		}
	}
	if _, err := os.Stat(audioPath); err != nil {
		return r.fail(&m, "diarizing", fmt.Errorf("the source WAV for diarization is unavailable: %w", err))
	}

	numSpeakers := remoteSpeakerCount(m.Metadata)
	if useMixed {
		numSpeakers = participantCount(m.Metadata)
	}
	turns, err := diarizeWithEngine(ctx, r.cfg.Inference, r.cfg.Diarization, audioPath, numSpeakers, func(percent int) {
		r.jobProgress(uid, "diarizing", percent)
	})
	if ctx.Err() != nil {
		return r.fail(&m, "diarizing", ctx.Err())
	}
	if err != nil {
		return r.fail(&m, "diarizing", err)
	}
	samples := selectSpeakerSamples(turns, 3)
	if len(turns) == 0 {
		setDefaultSystemSpeaker(diarizationSegments)
	} else {
		diarizationSegments = assignSystemSpeakers(diarizationSegments, turns)
	}

	var merged []model.Segment
	if useMixed {
		markMicrophoneOwner(diarizationSegments, micSegments, int64(r.cfg.Transcription.EchoTimeToleranceMS), r.cfg.Transcription.EchoTextSimilarity)
		merged = diarizationSegments
	} else {
		for index := range micSegments {
			micSegments[index].SpeakerID = "microphone_owner"
		}
		if r.cfg.Transcription.EchoDedupEnabled {
			micSegments, _ = deduplicateEcho(micSegments, diarizationSegments, int64(r.cfg.Transcription.EchoTimeToleranceMS), r.cfg.Transcription.EchoTextSimilarity)
		}
		merged = mergeSegments(micSegments, diarizationSegments)
	}
	if len(merged) == 0 {
		return r.fail(&m, "diarizing", errors.New("no transcript segments left after diarization"))
	}

	path := filepath.Join(dir, "transcript", r.name(m, "diarization")+".json")
	if err := writeJSONArtifact(path, map[string]any{"schema_version": 1, "meeting_uid": uid, "artifact_type": "diarization", "turns": turns}); err != nil {
		return r.fail(&m, "diarizing", err)
	}
	_, _ = r.addArtifact(uid, "diarization_json", path, "application/json", "generated")
	names, err := r.saveSpeakers(uid, merged, samples)
	if err != nil {
		return r.fail(&m, "diarizing", err)
	}
	m.SpeakerCount = len(names)
	m.Transcript = transcript.Format(merged, names)
	transcriptPath := filepath.Join(dir, "transcript", r.name(m, "transcript")+".md")
	if err := writeTextArtifact(transcriptPath, uid, "transcript", m.Transcript); err != nil {
		return r.fail(&m, "diarizing", err)
	}
	_, _ = r.addArtifact(uid, "transcript", transcriptPath, "text/markdown", "generated")
	r.job(uid, "diarizing", "completed", "")
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "completed", "completed", "", time.Now()
	if m.Summary != "" {
		m.SummaryStatus = "pending"
		if m.Metadata == nil {
			m.Metadata = make(map[string]string)
		}
		m.Metadata["summary_stale"] = "true"
	}
	return r.store.SaveMeeting(m)
}

// retrySummarization re-runs only the summarization stage.
func (r *Runner) retrySummarization(ctx context.Context, uid string) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	if strings.TrimSpace(m.Transcript) == "" {
		return r.fail(&m, "summarizing", errors.New("transcript is empty; re-run transcription first"))
	}
	m.Status, m.ProcessingStage, m.SummaryStatus, m.LastError, m.UpdatedAt = "processing", "summarizing", "processing", "", time.Now()
	_ = r.store.SaveMeeting(m)
	r.job(uid, "summarizing", "processing", "")
	r.jobProgress(uid, "summarizing", 10)
	summarizer, err := NewSummarizer(resolveSummaryConfig(r.cfg))
	var candidate string
	if err == nil {
		candidate, err = summarizer.Summarize(ctx, m.Transcript)
	}
	if ctx.Err() != nil {
		return r.fail(&m, "summarizing", ctx.Err())
	}
	if err != nil {
		m.SummaryStatus = "failed"
		return r.fail(&m, "summarizing", err)
	}
	r.jobProgress(uid, "summarizing", 90)
	path := filepath.Join(r.meetingDir(m), "summary", r.name(m, "summary")+".md")
	if err := writeTextArtifact(path, uid, "summary", candidate); err != nil {
		return r.fail(&m, "summarizing", err)
	}
	_, _ = r.addArtifact(uid, "summary", path, "text/markdown", "generated")
	m.Summary = candidate
	if m.Metadata != nil {
		delete(m.Metadata, "summary_stale")
	}
	m.Status, m.ProcessingStage, m.SummaryStatus, m.LastError, m.UpdatedAt = "completed", "completed", "completed", "", time.Now()
	r.job(uid, "summarizing", "completed", "")
	return r.store.SaveMeeting(m)
}

// retryEncoding re-runs only the audio encoding stage.
func (r *Runner) retryEncoding(ctx context.Context, uid string) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	jobs, _ := r.store.Jobs(uid)
	for _, job := range jobs {
		if job.Stage != "encoding" && job.Status == "failed" {
			return r.fail(&m, "encoding", fmt.Errorf("fix the “%s” stage first", job.Stage))
		}
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "processing", "encoding", "", time.Now()
	_ = r.store.SaveMeeting(m)
	r.job(uid, "encoding", "processing", "")
	r.jobProgress(uid, "encoding", 10)
	dir := r.meetingDir(m)
	micPath, sysPath := filepath.Join(dir, "audio", "microphone.wav"), filepath.Join(dir, "audio", "system.wav")
	mixedPath := filepath.Join(dir, "audio", r.name(m, "mixed")+".wav")
	if _, err := r.ensureCanonicalAudio(&m, micPath, sysPath); err != nil {
		return r.fail(&m, "encoding", err)
	}
	if _, err := os.Stat(mixedPath); err != nil {
		if err := audio.MixPCM16WAV(micPath, sysPath, mixedPath, r.cfg.Audio.MicrophoneGain, r.cfg.Audio.SystemGain); err != nil {
			return r.fail(&m, "encoding", err)
		}
	}
	r.jobProgress(uid, "encoding", 45)
	policy := strings.ToLower(strings.TrimSpace(r.cfg.Storage.AudioAfterProcessing))
	switch policy {
	case "delete":
		r.removeArtifactFiles(uid, micPath, sysPath, mixedPath)
	case "opus":
		path := strings.TrimSuffix(mixedPath, filepath.Ext(mixedPath)) + ".opus"
		if err := atomicfile.Generate(path, func(candidate string) error {
			return audio.EncodeWAVToOggOpusContext(ctx, mixedPath, candidate, uid, r.cfg.Storage.OpusBitrateKbps)
		}); err != nil {
			return r.fail(&m, "encoding", err)
		}
		if _, err := r.addArtifact(uid, "mixed_opus", path, "audio/ogg", "generated"); err != nil {
			return r.fail(&m, "encoding", err)
		}
		r.jobProgress(uid, "encoding", 90)
		r.removeArtifactFiles(uid, mixedPath, micPath, sysPath)
	case "mp3":
		path := strings.TrimSuffix(mixedPath, filepath.Ext(mixedPath)) + ".mp3"
		if err := atomicfile.Generate(path, func(candidate string) error {
			return r.encodeMP3(ctx, m, mixedPath, candidate)
		}); err != nil {
			return r.fail(&m, "encoding", err)
		}
		if _, err := r.addArtifact(uid, "mixed_mp3", path, "audio/mpeg", "generated"); err != nil {
			return r.fail(&m, "encoding", err)
		}
		r.removeArtifactFiles(uid, mixedPath, micPath, sysPath)
	default:
		r.preserveSourceWAVs(uid, micPath, sysPath, mixedPath)
	}
	r.job(uid, "encoding", "completed", "")
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "completed", "completed", "", time.Now()
	return r.store.SaveMeeting(m)
}
