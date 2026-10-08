// Package pipeline orchestrates post-recording processing: echo removal,
// transcription, diarization, merging, summarization and encoding.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"localmeetassist/internal/aec3"
	"localmeetassist/internal/atomicfile"
	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
	"localmeetassist/internal/modelmanager"
	"localmeetassist/internal/store"
	"localmeetassist/internal/transcript"
	"localmeetassist/internal/uuidv7"
)

// Runner executes and tracks the processing pipeline for each meeting.
type Runner struct {
	cfg     config.Config
	store   *store.Store
	logger  *log.Logger
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

type runPlan struct {
	transcription bool
	diarization   bool
	summary       bool
	encoding      bool
}

func (r *Runner) automaticPlan() runPlan {
	return runPlan{
		transcription: r.cfg.Transcription.AutoRun,
		diarization:   r.cfg.Diarization.AutoRun,
		summary:       r.cfg.Summary.AutoRun,
		encoding:      true,
	}
}

// New creates a Runner with a discarded logger.
func New(cfg config.Config, st *store.Store) *Runner {
	return NewWithLogger(cfg, st, log.New(io.Discard, "", 0))
}

// NewWithLogger creates a Runner that writes through logger.
func NewWithLogger(cfg config.Config, st *store.Store, logger *log.Logger) *Runner {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Runner{cfg: cfg, store: st, logger: logger, running: make(map[string]context.CancelFunc)}
}

// Start launches the automatic pipeline for a meeting.
func (r *Runner) Start(meetingUID string) error {
	return r.StartStage(meetingUID, "all")
}

// StartStage launches one stage, or the full pipeline when stage is "all".
func (r *Runner) StartStage(meetingUID, stage string) error {
	switch stage {
	case "all", "transcribing", "diarizing", "summarizing", "encoding":
	default:
		return fmt.Errorf("unsupported retry stage %q", stage)
	}
	r.mu.Lock()
	if _, active := r.running[meetingUID]; active {
		r.mu.Unlock()
		return errors.New("processing is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.running[meetingUID] = cancel
	r.mu.Unlock()
	if stage == "all" || stage == "transcribing" {
		for _, queuedStage := range []string{"transcribing", "diarizing", "deduplicating", "merging", "summarizing", "encoding"} {
			r.job(meetingUID, queuedStage, "queued", "")
		}
	} else {
		r.job(meetingUID, stage, "queued", "")
	}
	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			delete(r.running, meetingUID)
			r.mu.Unlock()
		}()
		var err error
		switch stage {
		case "diarizing":
			err = r.retryDiarization(ctx, meetingUID)
		case "summarizing":
			err = r.retrySummarization(ctx, meetingUID)
		case "encoding":
			err = r.retryEncoding(ctx, meetingUID)
		case "transcribing":
			err = r.runWithPlan(ctx, meetingUID, runPlan{transcription: true})
		default:
			err = r.Run(ctx, meetingUID)
		}
		if err != nil {
			appLogging.Errorf(r.logger, "stage retry finished with error uid=%s stage=%s error=%v", meetingUID, stage, err)
		}
	}()
	return nil
}

// IsRunning reports whether a pipeline is active for the meeting.
func (r *Runner) IsRunning(meetingUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, active := r.running[meetingUID]
	return active
}

// Cancel stops the active pipeline, ONNX work between inference calls, and an
// in-flight summary HTTP request.
func (r *Runner) Cancel(meetingUID string) bool {
	r.mu.Lock()
	cancel, active := r.running[meetingUID]
	r.mu.Unlock()
	if active {
		cancel()
	}
	return active
}

// UpdateConfig applies settings for subsequently started jobs. Updating while
// a job is active is rejected so one meeting is always processed with a
// consistent configuration snapshot.
func (r *Runner) UpdateConfig(cfg config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.running) != 0 {
		return errors.New("settings cannot be changed while a meeting is being processed")
	}
	r.cfg = cfg
	return nil
}

// AnyRunning reports whether any pipeline is active.
func (r *Runner) AnyRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running) != 0
}

// Run executes the pipeline synchronously using the automatic plan.
func (r *Runner) Run(ctx context.Context, uid string) error {
	return r.runWithPlan(ctx, uid, r.automaticPlan())
}

func (r *Runner) runWithPlan(ctx context.Context, uid string, plan runPlan) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	r.logger.Printf("pipeline start uid=%s transcription=%t diarization=%t summary=%t encoding=%t", uid, plan.transcription, plan.diarization, plan.summary, plan.encoding)
	dir := r.meetingDir(m)
	micPath, sysPath := filepath.Join(dir, "audio", "microphone.wav"), filepath.Join(dir, "audio", "system.wav")
	mixedPath := filepath.Join(dir, "audio", r.name(m, "mixed")+".wav")
	if plan.transcription || plan.diarization || plan.encoding {
		if _, prepareErr := r.ensureCanonicalAudio(&m, micPath, sysPath); prepareErr != nil {
			stage := "encoding"
			if plan.transcription {
				stage = "transcribing"
			} else if plan.diarization {
				stage = "diarizing"
			}
			return r.fail(&m, stage, prepareErr)
		}
	}
	micASRPath := micPath
	var warnings []string
	mixedReady := false
	ensureMixed := func() error {
		if mixedReady {
			return nil
		}
		if err := audio.MixPCM16WAV(micPath, sysPath, mixedPath, r.cfg.Audio.MicrophoneGain, r.cfg.Audio.SystemGain); err != nil {
			return err
		}
		mixedReady = true
		return nil
	}
	runtimePath := modelmanager.ResolveRuntimeLibrary(r.cfg.Inference.RuntimePath, r.cfg.Inference.RuntimeVersion)
	if override := strings.TrimSpace(os.Getenv("LOCALMEETASSIST_ONNXRUNTIME_PATH")); override != "" {
		runtimePath = override
	}
	gigaAM := &GigaAMONNX{Config: r.cfg.Transcription, RuntimePath: runtimePath}
	var transcriber Transcriber = gigaAM
	transcriberProgress := func(callback func(source string, completed, total int)) { gigaAM.ChunkProgress = callback }
	switch strings.ToLower(strings.TrimSpace(r.cfg.Transcription.Engine)) {
	case "whispercpp-native":
		whisper := &WhisperNative{Config: r.cfg.Transcription, RuntimePath: modelmanager.WhisperRuntimeLibraryPath(r.cfg.Inference.WhisperRuntimePath), ONNXRuntime: runtimePath}
		transcriber = whisper
		transcriberProgress = func(callback func(source string, completed, total int)) { whisper.ChunkProgress = callback }
	case "mock":
		transcriber = mockTranscriber{}
		transcriberProgress = func(func(source string, completed, total int)) {}
	}
	var segments []model.Segment
	var diarizationSamples map[string][]model.Sample
	if plan.transcription {
		if mode := strings.ToLower(strings.TrimSpace(r.cfg.Audio.EchoCancellation)); mode != "off" {
			cleanPath := filepath.Join(dir, "audio", r.name(m, "microphone_clean")+".wav")
			result, cleanErr := aec3.CleanWAV(micPath, sysPath, cleanPath, mode, r.cfg.Audio.EchoDelayMS)
			if cleanErr != nil {
				warnings = append(warnings, "echo cancellation failed; using the original microphone: "+cleanErr.Error())
				r.logger.Printf("aec3 failed uid=%s error=%v", uid, cleanErr)
			} else if result.Applied {
				micASRPath = cleanPath
				_, _ = r.addArtifact(uid, "mic_clean_wav", cleanPath, "audio/wav", "generated")
				r.logger.Printf("aec3 completed uid=%s correlation=%.3f input=%q output=%q", uid, result.Correlation, micPath, cleanPath)
			} else {
				r.logger.Printf("aec3 skipped uid=%s mode=%s correlation=%.3f", uid, mode, result.Correlation)
			}
		}
		m.Status, m.ProcessingStage, m.UpdatedAt = "processing", "transcribing", time.Now()
		m.LastError = ""
		_ = r.store.SaveMeeting(m)
		r.job(uid, "transcribing", "processing", "")
		sourceMode := strings.ToLower(strings.TrimSpace(r.cfg.Transcription.SourceMode))
		if override := strings.ToLower(strings.TrimSpace(m.Metadata["transcription_source_mode"])); override != "" {
			sourceMode = override
		}
		if sourceMode == "" {
			sourceMode = "auto"
		}
		importSources := pipelineImportSources(m.Metadata)
		forceMixed := sourceMode == "mixed" || importSources["mixed"]
		hasImportedSources := len(importSources) > 0
		useMicrophoneSource := !hasImportedSources || importSources["microphone"]
		useSystemSource := !hasImportedSources || importSources["system"]
		type progressRange struct{ start, end int }
		ranges := map[string]progressRange{}
		switch {
		case forceMixed && useMicrophoneSource:
			ranges["microphone"], ranges["mixed"] = progressRange{1, 40}, progressRange{40, 99}
		case forceMixed:
			ranges["mixed"] = progressRange{1, 99}
		case sourceMode == "separate":
			ranges["microphone"], ranges["system"] = progressRange{1, 50}, progressRange{50, 99}
		default:
			// In auto mode the final 29% is reserved for a possible mixed
			// fallback, so progress never has to move backwards.
			ranges["microphone"] = progressRange{1, 35}
			ranges["system"] = progressRange{35, 70}
			ranges["mixed"] = progressRange{70, 99}
		}
		transcriberProgress(func(source string, completed, total int) {
			r.logger.Printf("transcription block completed uid=%s source=%s block=%d/%d", uid, source, completed, total)
			span, ok := ranges[source]
			if !ok || total <= 0 {
				return
			}
			progress := span.start + (span.end-span.start)*completed/total
			r.jobProgress(uid, "transcribing", progress)
		})
		var micSegments []model.Segment
		var micErr error
		if !useMicrophoneSource {
			r.logger.Printf("transcription skipped uid=%s source=microphone reason=import_sources_%s", uid, m.Metadata["audio_import_sources"])
		} else {
			r.logger.Printf("transcription start uid=%s source=microphone path=%q", uid, micASRPath)
			micSegments, micErr = transcriber.Transcribe(ctx, micASRPath, "microphone")
			if ctx.Err() != nil {
				return r.fail(&m, "transcribing", ctx.Err())
			}
			if micErr != nil {
				r.logger.Printf("transcription failed uid=%s source=microphone error=%v", uid, micErr)
			} else {
				var dropped int
				micSegments, dropped = collapseRepeatedTokenRuns(micSegments, 2, 5)
				if dropped > 0 {
					warning := fmt.Sprintf("collapsed %d repeated token runs inside Whisper hypotheses in the microphone transcript.", dropped)
					warnings = append(warnings, warning)
					r.logger.Printf("transcription repeated hallucinations removed uid=%s source=microphone dropped=%d", uid, dropped)
				}
				micSegments, dropped = suppressDegenerateRuns(micSegments, 4)
				if dropped > 0 {
					warning := fmt.Sprintf("removed %d segments of continuous repeated Whisper hallucination in the microphone transcript.", dropped)
					warnings = append(warnings, warning)
					r.logger.Printf("transcription degenerate run removed uid=%s source=microphone dropped=%d", uid, dropped)
				}
				r.logger.Printf("transcription completed uid=%s source=microphone segments=%d", uid, len(micSegments))
				if path, artifactErr := r.saveSegments(m, "microphone_transcript", micSegments); artifactErr != nil {
					warnings = append(warnings, "could not save the raw microphone transcript: "+artifactErr.Error())
				} else {
					_, _ = r.addArtifact(uid, "microphone_transcript_json", path, "application/json", "generated")
				}
			}
		}
		var sysSegments []model.Segment
		var sysErr error
		systemDegenerate := false
		if !useSystemSource {
			r.logger.Printf("transcription skipped uid=%s source=system reason=import_sources_%s", uid, m.Metadata["audio_import_sources"])
		} else if forceMixed {
			r.logger.Printf("transcription skipped uid=%s source=system reason=source_mode_mixed", uid)
		} else {
			r.logger.Printf("transcription start uid=%s source=system path=%q", uid, sysPath)
			sysSegments, sysErr = transcriber.Transcribe(ctx, sysPath, "system")
			if ctx.Err() != nil {
				return r.fail(&m, "transcribing", ctx.Err())
			}
			if sysErr != nil {
				r.logger.Printf("transcription failed uid=%s source=system error=%v", uid, sysErr)
			} else {
				systemDegenerate = transcriptLooksDegenerate(sysSegments)
				var tokenDrops int
				sysSegments, tokenDrops = collapseRepeatedTokenRuns(sysSegments, 2, 5)
				if tokenDrops > 0 {
					warning := fmt.Sprintf("collapsed %d repeated token runs inside Whisper hypotheses in the system transcript.", tokenDrops)
					warnings = append(warnings, warning)
				}
				sysSegments, tokenDrops = suppressDegenerateRuns(sysSegments, 4)
				if tokenDrops > 0 {
					warning := fmt.Sprintf("removed %d segments of continuous repeated Whisper hallucination in the system transcript.", tokenDrops)
					warnings = append(warnings, warning)
				}
				var dropped int
				sysSegments, dropped = suppressRepeatedHallucinations(sysSegments, 2)
				if dropped > 0 {
					warning := fmt.Sprintf("removed %d mass repetitions of a short Whisper hypothesis in the system transcript.", dropped)
					warnings = append(warnings, warning)
					r.logger.Printf("transcription repeated hallucinations removed uid=%s source=system dropped=%d", uid, dropped)
				}
				r.logger.Printf("transcription completed uid=%s source=system segments=%d", uid, len(sysSegments))
				if path, artifactErr := r.saveSegments(m, "system_transcript", sysSegments); artifactErr != nil {
					warnings = append(warnings, "could not save the raw system transcript: "+artifactErr.Error())
				} else {
					_, _ = r.addArtifact(uid, "system_transcript_json", path, "application/json", "generated")
				}
			}
		}
		for i := range micSegments {
			micSegments[i].SpeakerID = "microphone_owner"
		}
		var mixedSegments []model.Segment
		useMixed := false
		fallbackNeeded := forceMixed || (sourceMode == "auto" && r.cfg.Transcription.MixedFallbackEnabled && (sysErr != nil || len(sysSegments) == 0 || systemDegenerate))
		if fallbackNeeded {
			reason := "system transcript is empty"
			if forceMixed {
				reason = "source_mode=mixed is configured"
			} else if sysErr != nil {
				reason = "system transcript error: " + sysErr.Error()
			} else if systemDegenerate {
				reason = "system transcript contains repeated text"
			}
			if mixErr := ensureMixed(); mixErr != nil {
				r.logger.Printf("mixed transcription unavailable uid=%s error=%v", uid, mixErr)
				if forceMixed {
					warnings = append(warnings, "could not prepare mixed.wav: "+mixErr.Error())
				}
			} else {
				r.logger.Printf("transcription fallback start uid=%s source=mixed reason=%q path=%q", uid, reason, mixedPath)
				var mixedErr error
				mixedSegments, mixedErr = transcriber.Transcribe(ctx, mixedPath, "mixed")
				if ctx.Err() != nil {
					return r.fail(&m, "transcribing", ctx.Err())
				}
				if mixedErr != nil {
					r.logger.Printf("transcription fallback failed uid=%s source=mixed error=%v", uid, mixedErr)
					if forceMixed {
						warnings = append(warnings, "mixed.wav transcription failed: "+mixedErr.Error())
					}
				} else if len(mixedSegments) > 0 {
					var tokenDrops int
					mixedSegments, tokenDrops = collapseRepeatedTokenRuns(mixedSegments, 2, 5)
					if tokenDrops > 0 {
						warning := fmt.Sprintf("collapsed %d repeated token runs inside Whisper hypotheses in the mixed transcript.", tokenDrops)
						warnings = append(warnings, warning)
					}
					mixedSegments, tokenDrops = suppressDegenerateRuns(mixedSegments, 4)
					if tokenDrops > 0 {
						warning := fmt.Sprintf("removed %d segments of continuous repeated Whisper hallucination in the mixed transcript.", tokenDrops)
						warnings = append(warnings, warning)
					}
					var dropped int
					mixedSegments, dropped = suppressRepeatedHallucinations(mixedSegments, 2)
					if dropped > 0 {
						warning := fmt.Sprintf("removed %d mass repetitions of a short Whisper hypothesis in the mixed transcript.", dropped)
						warnings = append(warnings, warning)
						r.logger.Printf("transcription repeated hallucinations removed uid=%s source=mixed dropped=%d", uid, dropped)
					}
					useMixed = true
					if !forceMixed {
						warnings = append(warnings, "the system stream was not recognized reliably; the single mixed.wav was used ("+reason+").")
					}
					r.logger.Printf("transcription fallback completed uid=%s source=mixed segments=%d", uid, len(mixedSegments))
					if path, artifactErr := r.saveSegments(m, "mixed_transcript", mixedSegments); artifactErr != nil {
						warnings = append(warnings, "could not save the raw mixed transcript: "+artifactErr.Error())
					} else {
						_, _ = r.addArtifact(uid, "mixed_transcript_json", path, "application/json", "generated")
					}
				} else if forceMixed {
					warnings = append(warnings, "mixed.wav transcription contains no speech.")
				}
			}
		}

		if forceMixed && !useMixed && micErr != nil {
			return r.fail(&m, "transcribing", fmt.Errorf("microphone: %v; mixed transcription unavailable", micErr))
		}
		if !useMixed && micErr != nil && sysErr != nil {
			return r.fail(&m, "transcribing", fmt.Errorf("microphone: %v; system: %v", micErr, sysErr))
		}
		if !useMixed && micErr != nil {
			warnings = append(warnings, "microphone transcription failed: "+micErr.Error())
		}
		if !useMixed && sysErr != nil {
			warnings = append(warnings, "system audio transcription failed: "+sysErr.Error())
		}
		// Transcription artifacts are complete at this point. Diarization is a
		// separate job and must not leave the transcription progress spinning.
		r.job(uid, "transcribing", "completed", "")

		diarizationPath := sysPath
		diarizationSegments := sysSegments
		if useMixed {
			diarizationPath = mixedPath
			diarizationSegments = mixedSegments
		}
		if len(diarizationSegments) > 0 && plan.diarization {
			m.ProcessingStage, m.UpdatedAt = "diarizing", time.Now()
			_ = r.store.SaveMeeting(m)
			r.job(uid, "diarizing", "processing", "")
			r.logger.Printf("diarization start uid=%s path=%q", uid, diarizationPath)
			var turns []SpeakerTurn
			var diarizationErr error
			numSpeakers := remoteSpeakerCount(m.Metadata)
			if useMixed {
				numSpeakers = participantCount(m.Metadata)
			}
			turns, diarizationErr = diarizeWithEngine(ctx, r.cfg.Inference, r.cfg.Diarization, diarizationPath, numSpeakers, func(percent int) {
				r.jobProgress(uid, "diarizing", percent)
			})
			if ctx.Err() != nil {
				return r.fail(&m, "diarizing", ctx.Err())
			}
			if diarizationErr != nil {
				return r.fail(&m, "diarizing", diarizationErr)
			} else {
				diarizationSegments = assignSystemSpeakers(diarizationSegments, turns)
				diarizationSamples = selectSpeakerSamples(turns, 3)
				r.job(uid, "diarizing", "completed", "")
				r.logger.Printf("diarization completed uid=%s turns=%d speakers=%d", uid, len(turns), countTurnSpeakers(turns))
				path := filepath.Join(dir, "transcript", r.name(m, "diarization")+".json")
				if artifactErr := writeJSONArtifact(path, map[string]any{"schema_version": 1, "meeting_uid": uid, "artifact_type": "diarization", "turns": turns}); artifactErr != nil {
					warnings = append(warnings, "could not save the diarization result: "+artifactErr.Error())
				} else {
					_, _ = r.addArtifact(uid, "diarization_json", path, "application/json", "generated")
				}
			}
		} else {
			setDefaultSystemSpeaker(diarizationSegments)
			if plan.diarization {
				r.job(uid, "diarizing", "skipped", "system transcript is empty")
			} else {
				r.job(uid, "diarizing", "skipped", "automatic diarization is disabled")
			}
		}

		if useMixed {
			mixedSegments = diarizationSegments
			markMicrophoneOwner(mixedSegments, micSegments, int64(r.cfg.Transcription.EchoTimeToleranceMS), r.cfg.Transcription.EchoTextSimilarity)
			segments = mixedSegments
			r.job(uid, "deduplicating", "skipped", "mixed fallback is a single transcription stream")
		} else if r.cfg.Transcription.EchoDedupEnabled && len(micSegments) > 0 && len(diarizationSegments) > 0 {
			sysSegments = diarizationSegments
			m.ProcessingStage, m.UpdatedAt = "deduplicating", time.Now()
			_ = r.store.SaveMeeting(m)
			r.job(uid, "deduplicating", "processing", "")
			r.jobProgress(uid, "deduplicating", 50)
			var dropped []model.Segment
			micSegments, dropped = deduplicateEcho(micSegments, sysSegments, int64(r.cfg.Transcription.EchoTimeToleranceMS), r.cfg.Transcription.EchoTextSimilarity)
			r.job(uid, "deduplicating", "completed", "")
			r.logger.Printf("echo deduplication completed uid=%s kept=%d dropped=%d threshold=%.2f tolerance_ms=%d", uid, len(micSegments), len(dropped), r.cfg.Transcription.EchoTextSimilarity, r.cfg.Transcription.EchoTimeToleranceMS)
			segments = mergeSegments(micSegments, sysSegments)
		} else {
			sysSegments = diarizationSegments
			r.job(uid, "deduplicating", "skipped", "echo deduplication is disabled or one stream is empty")
			segments = mergeSegments(micSegments, sysSegments)
		}
	} else {
		r.job(uid, "transcribing", "skipped", "automatic transcription is disabled")
		r.job(uid, "diarizing", "skipped", "transcription did not run")
		r.job(uid, "deduplicating", "skipped", "transcription did not run")
	}

	if len(segments) > 0 {
		m.ProcessingStage = "merging"
		_ = r.store.SaveMeeting(m)
		r.job(uid, "merging", "processing", "")
		r.jobProgress(uid, "merging", 25)
		names, speakerErr := r.saveSpeakers(uid, segments, diarizationSamples)
		if speakerErr != nil {
			return r.fail(&m, "merging", speakerErr)
		}
		m.SpeakerCount = len(names)
		m.Transcript = transcript.Format(segments, names)
		r.jobProgress(uid, "merging", 75)
		path := filepath.Join(dir, "transcript", r.name(m, "transcript")+".md")
		if err := writeTextArtifact(path, uid, "transcript", m.Transcript); err != nil {
			return r.fail(&m, "merging", err)
		}
		if _, err := r.addArtifact(uid, "transcript", path, "text/markdown", "generated"); err != nil {
			return r.fail(&m, "merging", err)
		}
		r.job(uid, "merging", "completed", "")
	}

	if plan.summary && m.Transcript != "" {
		if ctx.Err() != nil {
			return r.fail(&m, "summarizing", ctx.Err())
		}
		m.ProcessingStage = "summarizing"
		m.SummaryStatus = "processing"
		_ = r.store.SaveMeeting(m)
		r.job(uid, "summarizing", "processing", "")
		r.jobProgress(uid, "summarizing", 10)
		s, createErr := NewSummarizer(resolveSummaryConfig(r.cfg))
		var summary string
		if createErr == nil {
			summary, createErr = s.Summarize(ctx, m.Transcript)
		}
		if ctx.Err() != nil {
			return r.fail(&m, "summarizing", ctx.Err())
		}
		if createErr != nil {
			m.SummaryStatus = "failed"
			return r.fail(&m, "summarizing", createErr)
		} else {
			r.jobProgress(uid, "summarizing", 90)
			m.Summary, m.SummaryStatus = summary, "completed"
			if m.Metadata != nil {
				delete(m.Metadata, "summary_stale")
			}
			path := filepath.Join(dir, "summary", r.name(m, "summary")+".md")
			if err := writeTextArtifact(path, uid, "summary", summary); err == nil {
				_, _ = r.addArtifact(uid, "summary", path, "text/markdown", "generated")
			}
			r.job(uid, "summarizing", "completed", "")
		}
	} else {
		if m.Summary == "" {
			m.SummaryStatus = "disabled"
		}
		reason := "automatic summarization is disabled"
		if plan.summary && m.Transcript == "" {
			reason = "the final transcript is missing"
		}
		r.job(uid, "summarizing", "skipped", reason)
	}

	m.ProcessingStage = "encoding"
	_ = r.store.SaveMeeting(m)
	if ctx.Err() != nil {
		return r.fail(&m, "encoding", ctx.Err())
	}
	policy := strings.ToLower(strings.TrimSpace(r.cfg.Storage.AudioAfterProcessing))
	if !plan.encoding {
		r.preserveSourceWAVs(uid, micPath, sysPath, mixedPath)
		r.job(uid, "encoding", "skipped", "another stage was started manually; source WAV files were kept")
	} else if policy == "delete" && len(warnings) > 0 {
		r.preserveSourceWAVs(uid, micPath, sysPath, mixedPath)
		r.job(uid, "encoding", "skipped", "source WAV files were kept because of processing warnings")
	} else if policy == "delete" {
		r.job(uid, "encoding", "skipped", "audio retention policy is delete")
		r.removeArtifactFiles(uid, micPath, sysPath, mixedPath)
	} else {
		r.job(uid, "encoding", "processing", "")
		r.jobProgress(uid, "encoding", 10)
		if err := ensureMixed(); err != nil {
			return r.fail(&m, "encoding", err)
		}
		r.jobProgress(uid, "encoding", 45)
		if policy == "opus" {
			opusPath := strings.TrimSuffix(mixedPath, filepath.Ext(mixedPath)) + ".opus"
			if err := atomicfile.Generate(opusPath, func(candidate string) error {
				return audio.EncodeWAVToOggOpusContext(ctx, mixedPath, candidate, uid, r.cfg.Storage.OpusBitrateKbps)
			}); err != nil {
				return r.fail(&m, "encoding", err)
			}
			if _, err := r.addArtifact(uid, "mixed_opus", opusPath, "audio/ogg", "generated"); err != nil {
				return r.fail(&m, "encoding", err)
			}
			r.jobProgress(uid, "encoding", 90)
			if len(warnings) == 0 {
				r.removeArtifactFiles(uid, mixedPath, micPath, sysPath)
			} else {
				r.preserveSourceWAVs(uid, micPath, sysPath, mixedPath)
			}
		} else if policy == "mp3" {
			mp3Path := strings.TrimSuffix(mixedPath, filepath.Ext(mixedPath)) + ".mp3"
			if err := atomicfile.Generate(mp3Path, func(candidate string) error {
				return r.encodeMP3(ctx, m, mixedPath, candidate)
			}); err != nil {
				return r.fail(&m, "encoding", err)
			}
			if _, err := r.addArtifact(uid, "mixed_mp3", mp3Path, "audio/mpeg", "generated"); err != nil {
				return r.fail(&m, "encoding", err)
			}
			if len(warnings) == 0 {
				r.removeArtifactFiles(uid, mixedPath, micPath, sysPath)
			} else {
				r.preserveSourceWAVs(uid, micPath, sysPath, mixedPath)
			}
		} else {
			if _, err := r.addArtifact(uid, "mixed_wav", mixedPath, "audio/wav", "generated"); err != nil {
				return r.fail(&m, "encoding", err)
			}
			_, _ = r.addArtifact(uid, "mic_wav", micPath, "audio/wav", "generated")
			_, _ = r.addArtifact(uid, "system_wav", sysPath, "audio/wav", "generated")
		}
		r.job(uid, "encoding", "completed", "")
	}

	m.Status, m.ProcessingStage, m.UpdatedAt = "completed", "completed", time.Now()
	m.LastError = strings.Join(warnings, "\n")
	if len(warnings) > 0 {
		m.Status = "warning"
	}
	metaPath := filepath.Join(dir, "metadata.json")
	_ = writeMetadata(metaPath, m)
	_, _ = r.addArtifact(uid, "metadata", metaPath, "application/json", "generated")
	if err := r.store.SaveMeeting(m); err != nil {
		return err
	}
	r.logger.Printf("pipeline completed uid=%s status=%s speakers=%d warnings=%d", uid, m.Status, m.SpeakerCount, len(warnings))
	return nil
}

func (r *Runner) preserveSourceWAVs(meetingUID, micPath, sysPath, mixedPath string) {
	for _, item := range []struct {
		kind string
		path string
	}{{"mic_wav", micPath}, {"system_wav", sysPath}, {"mixed_wav", mixedPath}} {
		if info, err := os.Stat(item.path); err == nil && info.Size() > 44 {
			_, _ = r.addArtifact(meetingUID, item.kind, item.path, "audio/wav", "generated")
		}
	}
}

// ensureCanonicalAudio recreates the canonical PCM16/16 kHz WAV pair needed
// by transcription, diarization and encoding. This makes an Opus-only or
// MP3-only archived meeting processable again without deleting the archive.
func (r *Runner) ensureCanonicalAudio(meeting *model.Meeting, micPath, sysPath string) (bool, error) {
	micValid := validCanonicalWAV(micPath)
	sysValid := validCanonicalWAV(sysPath)
	changed := false
	for _, candidate := range []struct {
		path  string
		valid *bool
	}{{micPath, &micValid}, {sysPath, &sysValid}} {
		if *candidate.valid {
			continue
		}
		if info, err := os.Stat(candidate.path); err == nil && info.Size() > 44 {
			if err := audio.NormalizeImportedAudio(candidate.path, candidate.path); err != nil {
				return false, fmt.Errorf("could not convert %s to PCM16 mono 16 kHz: %w", filepath.Base(candidate.path), err)
			}
			*candidate.valid = true
			changed = true
		}
	}
	if micValid && sysValid {
		if changed {
			_, _ = r.addArtifact(meeting.UID, "mic_wav", micPath, "audio/wav", "restored")
			_, _ = r.addArtifact(meeting.UID, "system_wav", sysPath, "audio/wav", "restored")
		}
		return changed, nil
	}
	if err := os.MkdirAll(filepath.Dir(micPath), 0o700); err != nil {
		return false, err
	}
	if meeting.Metadata == nil {
		meeting.Metadata = make(map[string]string)
	}
	if micValid && !sysValid {
		if err := audio.CreateSilentPCM16WAVLike(micPath, sysPath); err != nil {
			return false, fmt.Errorf("could not create the system WAV companion: %w", err)
		}
		meeting.Metadata["audio_import_sources"] = "microphone"
		meeting.Metadata["transcription_source_mode"] = "separate"
	} else if sysValid && !micValid {
		if err := audio.CreateSilentPCM16WAVLike(sysPath, micPath); err != nil {
			return false, fmt.Errorf("could not create the microphone WAV companion: %w", err)
		}
		meeting.Metadata["audio_import_sources"] = "system"
		meeting.Metadata["transcription_source_mode"] = "separate"
	} else {
		artifacts, err := r.store.Artifacts(meeting.UID)
		if err != nil {
			return false, err
		}
		var compressed string
		for index := len(artifacts) - 1; index >= 0; index-- {
			artifact := artifacts[index]
			ext := strings.ToLower(filepath.Ext(artifact.Path))
			if artifact.Type == "mixed_opus" || artifact.Type == "mixed_mp3" || ext == ".opus" || ext == ".ogg" || ext == ".mp3" {
				if info, statErr := os.Stat(artifact.Path); statErr == nil && info.Size() > 0 {
					compressed = artifact.Path
					break
				}
			}
		}
		if compressed == "" {
			return false, errors.New("source WAV files are missing; add a WAV, Opus or MP3 to the meeting files")
		}
		if err := audio.NormalizeImportedAudio(compressed, sysPath); err != nil {
			return false, fmt.Errorf("could not restore WAV from %s: %w", filepath.Base(compressed), err)
		}
		if err := audio.CreateSilentPCM16WAVLike(sysPath, micPath); err != nil {
			return false, fmt.Errorf("could not create the WAV companion: %w", err)
		}
		meeting.Metadata["audio_import_sources"] = "mixed"
		meeting.Metadata["audio_import_type"] = "mixed"
		meeting.Metadata["transcription_source_mode"] = "mixed"
		r.logger.Printf("compressed audio restored uid=%s source=%q microphone=%q system=%q", meeting.UID, compressed, micPath, sysPath)
	}
	meeting.UpdatedAt = time.Now()
	if err := r.store.SaveMeeting(*meeting); err != nil {
		return false, err
	}
	_, _ = r.addArtifact(meeting.UID, "mic_wav", micPath, "audio/wav", "restored")
	_, _ = r.addArtifact(meeting.UID, "system_wav", sysPath, "audio/wav", "restored")
	return true, nil
}

func validCanonicalWAV(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	info, err := audio.ReadWAVInfo(file)
	_ = file.Close()
	if err != nil || info.SampleRate != 16000 || info.Channels != 1 || info.BitsPerSample != 16 {
		return false
	}
	duration, err := audio.PCM16WAVDurationMS(path)
	return err == nil && duration > 0
}

func (r *Runner) fail(m *model.Meeting, stage string, err error) error {
	if errors.Is(err, context.Canceled) {
		return r.markCanceled(m, stage)
	}
	m.Status = "failed"
	m.ProcessingStage = stage
	m.LastError = err.Error()
	m.UpdatedAt = time.Now()
	_ = r.store.SaveMeeting(*m)
	r.job(m.UID, stage, "failed", err.Error())
	appLogging.Errorf(r.logger, "pipeline failed uid=%s stage=%s error=%v", m.UID, stage, err)
	return err
}

func (r *Runner) markCanceled(m *model.Meeting, stage string) error {
	const message = "processing was stopped by the user"
	m.Status = "canceled"
	m.ProcessingStage = "canceled"
	if stage == "summarizing" {
		m.SummaryStatus = "canceled"
	}
	m.LastError = message
	m.UpdatedAt = time.Now()
	dir := r.meetingDir(*m)
	r.preserveSourceWAVs(m.UID,
		filepath.Join(dir, "audio", "microphone.wav"),
		filepath.Join(dir, "audio", "system.wav"),
		filepath.Join(dir, "audio", r.name(*m, "mixed")+".wav"),
	)
	_ = r.store.SaveMeeting(*m)
	jobs, _ := r.store.Jobs(m.UID)
	for _, job := range jobs {
		if job.Status == "processing" || job.Status == "queued" {
			r.job(m.UID, job.Stage, "canceled", message)
		}
	}
	appLogging.Warnf(r.logger, "pipeline canceled uid=%s stage=%s", m.UID, stage)
	return context.Canceled
}
func (r *Runner) job(uid, stage, status, lastErr string) {
	now := time.Now()
	jobs, _ := r.store.Jobs(uid)
	attempts := 1
	existingProgress := 0
	for _, j := range jobs {
		if j.Stage == stage {
			attempts = j.Attempts
			existingProgress = j.Progress
			if status == "processing" && j.Status != "processing" {
				attempts++
			}
		}
	}
	progress := 0
	if status == "completed" || status == "skipped" {
		progress = 100
	} else if status == "failed" || status == "canceled" {
		progress = existingProgress
	} else if status == "processing" {
		progress = 1
	}
	_ = r.store.SaveJob(model.Job{UID: uuidv7.New(), MeetingUID: uid, Stage: stage, Status: status, Attempts: attempts, LastError: lastErr, Progress: progress, CreatedAt: now, UpdatedAt: now})
}

func (r *Runner) jobProgress(uid, stage string, progress int) {
	if progress < 0 {
		progress = 0
	}
	if progress > 99 {
		progress = 99
	}
	jobs, _ := r.store.Jobs(uid)
	for _, job := range jobs {
		if job.Stage != stage || job.Status != "processing" || progress <= job.Progress {
			continue
		}
		job.Progress = progress
		job.UpdatedAt = time.Now()
		_ = r.store.SaveJob(job)
		return
	}
}

func (r *Runner) meetingDir(m model.Meeting) string {
	return filepath.Join(r.cfg.App.DataDir, "meetings", m.StartedAt.Format("2006"), m.StartedAt.Format("01"), m.UID)
}
func (r *Runner) name(m model.Meeting, artifactType string) string {
	v := r.cfg.Storage.FileNameTemplate
	if v == "" {
		v = "{{date}}_{{time}}_{{title_slug}}_{{uid_short}}_{{artifact_type}}"
	}
	repl := map[string]string{"{{date}}": m.StartedAt.Format(r.cfg.Storage.DateFormat), "{{time}}": m.StartedAt.Format(r.cfg.Storage.TimeFormat), "{{title_slug}}": slug(m.Title), "{{uid_short}}": first(m.UID, 8), "{{artifact_type}}": artifactType}
	for k, value := range repl {
		v = strings.ReplaceAll(v, k, value)
	}
	return slugFile(v)
}

func (r *Runner) addArtifact(meetingUID, kind, path, mime, source string) (model.Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return model.Artifact{}, err
	}
	a := model.Artifact{UID: uuidv7.New(), MeetingUID: meetingUID, Type: kind, Path: path, MIMEType: mime, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil)), Source: source, CreatedAt: time.Now()}
	_, _ = r.store.DeleteArtifactsByPaths(meetingUID, path)
	return a, r.store.SaveArtifact(a)
}

func (r *Runner) removeArtifactFiles(meetingUID string, paths ...string) {
	_, _ = r.store.DeleteArtifactsByPaths(meetingUID, paths...)
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func (r *Runner) saveSegments(m model.Meeting, artifactType string, segments []model.Segment) (string, error) {
	path := filepath.Join(r.meetingDir(m), "transcript", r.name(m, artifactType)+".json")
	err := writeJSONArtifact(path, map[string]any{
		"schema_version": 1,
		"meeting_uid":    m.UID,
		"artifact_type":  artifactType,
		"segments":       segments,
	})
	return path, err
}

func (r *Runner) saveSpeakers(meetingUID string, segments []model.Segment, diarizationSamples map[string][]model.Sample) (map[string]string, error) {
	ownerName := config.DefaultMicrophoneOwnerName(r.cfg.App.Language)
	if meeting, loadErr := r.store.Meeting(meetingUID); loadErr == nil {
		if configured := strings.TrimSpace(meeting.Metadata["microphone_owner_name"]); configured != "" {
			ownerName = configured
		}
	}
	existing, err := r.store.Speakers(meetingUID)
	if err != nil {
		return nil, err
	}
	existingNames := make(map[string]string, len(existing))
	for _, speaker := range existing {
		existingNames[speaker.ID] = speaker.DisplayName
	}
	speakers := make(map[string]*model.Speaker)
	for index := range segments {
		segment := &segments[index]
		if segment.ID == "" {
			segment.ID = uuidv7.New()
		}
		if segment.SpeakerID == "" {
			continue
		}
		speaker := speakers[segment.SpeakerID]
		if speaker == nil {
			displayName := existingNames[segment.SpeakerID]
			if displayName == "" {
				displayName = segment.SpeakerID
				if segment.SpeakerID == "microphone_owner" {
					displayName = ownerName
				}
			}
			speaker = &model.Speaker{MeetingUID: meetingUID, ID: segment.SpeakerID, DisplayName: displayName, Source: segment.Source}
			speakers[segment.SpeakerID] = speaker
		}
		speaker.Fragments = append(speaker.Fragments, model.Sample{ID: segment.ID, StartMS: segment.StartMS, EndMS: segment.EndMS, Source: segment.Source, Text: segment.Text})
		if len(speaker.Samples) < 3 && len(diarizationSamples[segment.SpeakerID]) == 0 {
			end := segment.EndMS
			if end > segment.StartMS+10000 {
				end = segment.StartMS + 10000
			}
			if end > segment.StartMS {
				speaker.Samples = append(speaker.Samples, model.Sample{StartMS: segment.StartMS, EndMS: end})
			}
		}
	}
	for id, speaker := range speakers {
		if samples := diarizationSamples[id]; len(samples) > 0 && id != "microphone_owner" {
			speaker.Samples = append([]model.Sample(nil), samples...)
		}
	}

	names := make(map[string]string, len(speakers))
	items := make([]model.Speaker, 0, len(speakers))
	for _, speaker := range speakers {
		items = append(items, *speaker)
		names[speaker.ID] = speaker.DisplayName
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if err := r.store.ReplaceSpeakersAndSegments(meetingUID, items, segments); err != nil {
		return nil, err
	}
	return names, nil
}

func selectSpeakerSamples(turns []SpeakerTurn, limit int) map[string][]model.Sample {
	grouped := make(map[string][]SpeakerTurn)
	for _, turn := range turns {
		if turn.EndMS-turn.StartMS >= 500 {
			grouped[turn.SpeakerID] = append(grouped[turn.SpeakerID], turn)
		}
	}
	result := make(map[string][]model.Sample, len(grouped))
	for speakerID, candidates := range grouped {
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].EndMS-candidates[i].StartMS > candidates[j].EndMS-candidates[j].StartMS
		})
		for _, turn := range candidates {
			if len(result[speakerID]) >= limit {
				break
			}
			end := turn.EndMS
			if end > turn.StartMS+10000 {
				end = turn.StartMS + 10000
			}
			result[speakerID] = append(result[speakerID], model.Sample{StartMS: turn.StartMS, EndMS: end})
		}
		sort.Slice(result[speakerID], func(i, j int) bool {
			return result[speakerID][i].StartMS < result[speakerID][j].StartMS
		})
	}
	return result
}

func pipelineImportSources(metadata map[string]string) map[string]bool {
	sources := make(map[string]bool)
	for _, value := range strings.Split(metadata["audio_import_sources"], ",") {
		switch value = strings.ToLower(strings.TrimSpace(value)); value {
		case "microphone", "system", "mixed":
			sources[value] = true
		}
	}
	return sources
}

// remoteSpeakerCount converts the meeting-level total participant count into
// the number of voices expected in system.wav. The microphone owner is always
// represented by the separate microphone stream.
func remoteSpeakerCount(metadata map[string]string) int {
	if total, err := strconv.Atoi(metadata["participant_count"]); err == nil && total > 1 {
		return total - 1
	}
	return 0
}

func participantCount(metadata map[string]string) int {
	if total, err := strconv.Atoi(metadata["participant_count"]); err == nil && total > 0 {
		return total
	}
	return 0
}

func setDefaultSystemSpeaker(segments []model.Segment) {
	for index := range segments {
		segments[index].SpeakerID = "SPEAKER_00"
	}
}

func countTurnSpeakers(turns []SpeakerTurn) int {
	unique := make(map[string]struct{})
	for _, turn := range turns {
		unique[turn.SpeakerID] = struct{}{}
	}
	return len(unique)
}

func (r *Runner) encodeMP3(ctx context.Context, m model.Meeting, input, output string) error {
	command := strings.TrimSpace(r.cfg.Storage.EncoderCommand)
	if command == "" {
		return errors.New("storage.encoder_command is required for mp3")
	}
	if _, err := os.Stat(command); err != nil {
		return fmt.Errorf("MP3 encoder unavailable: %w", err)
	}
	args := []string{"--silent", "-b", strconv.Itoa(r.cfg.Storage.MP3BitrateKbps), "--tt", m.Title, "--tc", "meeting_uid=" + m.UID, input, output}
	var outputLog strings.Builder
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout = &outputLog
	cmd.Stderr = &outputLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("MP3 encoder failed: %w: %s", err, tail(outputLog.String(), 800))
	}
	return nil
}

func writeTextArtifact(path, uid, kind, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("---\nmeeting_uid: %s\nartifact_type: %s\ncreated_at: %s\nschema_version: 1\n---\n\n%s\n", uid, kind, time.Now().UTC().Format(time.RFC3339Nano), content)
	return atomicfile.Write(path, []byte(body), 0o600)
}
func writeJSONArtifact(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

func writeMetadata(path string, m model.Meeting) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{"schema_version": 1, "meeting_uid": m.UID, "title": m.Title, "started_at": m.StartedAt, "finished_at": m.FinishedAt, "status": m.Status}, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
func slug(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	var b strings.Builder
	dash := false
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func slugFile(v string) string {
	v = slug(v)
	if v == "" {
		return "meeting"
	}
	return v
}
func first(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}
