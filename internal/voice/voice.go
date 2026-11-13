// Package voice enrolls and matches speaker voiceprints. A voiceprint is a
// normalized WeSpeaker embedding plus a short Ogg/Opus reference clip. The
// package deliberately keeps biometric vectors out of the meeting speaker
// cards: profiles live in their own bbolt bucket and are linked by Person ID.
package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/inference"
	"localmeetassist/internal/model"
	"localmeetassist/internal/modelmanager"
	"localmeetassist/internal/store"
	"localmeetassist/internal/uuidv7"
)

const (
	// MaxSamplesPerPerson bounds the profile. Ten clean samples are enough to
	// cover different days and channels without letting one meeting dominate.
	MaxSamplesPerPerson = 10
	// MaxSamplesPerMeeting reserves room for later meetings and prevents a
	// single recording from filling the whole profile.
	MaxSamplesPerMeeting = 4
	// MaturitySamples and MaturityMeetings gate automatic name substitution.
	MaturitySamples  = 8
	MaturityMeetings = 2
	// AutoMargin is the required gap between the best and the second profile.
	AutoMargin = 0.05

	minSampleMS        = 1500
	maxSampleMS        = 10000
	linkThreshold      = 0.55
	duplicateThreshold = 0.98
	minRMS             = 0.0015
	minSpeechRatio     = 0.35
	maxClipped         = 0.02
)

// Warnings are stable codes translated by the Web UI.
const (
	WarnNoCleanSamples        = "no_clean_samples"
	WarnAudioUnavailable      = "audio_unavailable"
	WarnSomeSamplesRejected   = "some_samples_rejected"
	WarnProfileNeedsQualifier = "profile_needs_qualifier"
	WarnAlreadyEnoughSamples  = "already_enough_samples"
	WarnInconsistentSamples   = "inconsistent_samples"
)

// ErrAudioUnavailable means the source WAV is gone or the meeting is archived.
var ErrAudioUnavailable = errors.New("meeting audio is unavailable for voice enrollment")

// EnrollRequest describes one manual speaker rename.
type EnrollRequest struct {
	MeetingUID string
	SpeakerID  string
	// SpeakerName is the user-visible name, possibly with a namesake qualifier.
	SpeakerName string
	// Source is "system" or "mixed".
	Source    string
	AudioPath string
	// ExistingPersonID is the automatic match that was shown in the UI.
	ExistingPersonID string
	// ExplicitPersonID comes from a clicked suggestion or the same-name prompt.
	ExplicitPersonID string
	Candidates       []model.VoiceCandidate
	Turns            []model.SpeakerTurn
	Segments         []model.Segment
}

// EnrollResult reports what happened. Warnings are stable codes.
type EnrollResult struct {
	PersonID   string   `json:"person_id,omitempty"`
	PersonName string   `json:"person_name,omitempty"`
	Added      int      `json:"added"`
	Replaced   int      `json:"replaced,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// MatchResult is one profile match for a meeting speaker.
type MatchResult struct {
	PersonID   string
	Name       string
	Score      float64
	Auto       bool
	Candidates []model.SpeakerMatch
}

type preparedSample struct {
	candidate model.VoiceCandidate
	vector    []float32
	quality   model.VoiceQuality
	tempPath  string
}

type profileCandidate struct {
	person   model.Person
	score    float64
	count    int
	meetings int
}

type hashEntry struct {
	size int64
	mod  time.Time
	hash string
}

// Service owns the ONNX embedding session shared by enrollment and matching.
type Service struct {
	store  *store.Store
	logger *log.Logger

	sessionMu    sync.Mutex
	session      *ort.DynamicAdvancedSession
	sessionModel string

	hashMu sync.Mutex
	hashes map[string]hashEntry
}

// New creates a voice service. The ONNX session is opened lazily on the first
// enrollment, so a server without the setting enabled never loads the model.
func New(st *store.Store, logger *log.Logger) *Service {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Service{store: st, logger: logger, hashes: make(map[string]hashEntry)}
}

// Close releases the ONNX session.
func (s *Service) Close() {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.session != nil {
		s.session.Destroy()
		s.session = nil
		s.sessionModel = ""
	}
}

// ModelHash returns the SHA-256 of the embedding model, cached by size and
// modification time. Vectors from different models are not comparable.
func (s *Service) ModelHash(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	s.hashMu.Lock()
	defer s.hashMu.Unlock()
	if entry, ok := s.hashes[path]; ok && entry.size == info.Size() && entry.mod.Equal(info.ModTime()) {
		return entry.hash, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	s.hashes[path] = hashEntry{size: info.Size(), mod: info.ModTime(), hash: hash}
	return hash, nil
}

// Match compares the cluster centroids of a new meeting with the saved
// profiles. It never changes the store; the pipeline decides what to apply.
func (s *Service) Match(cfg config.Config, centroids map[string][]float32) map[string]MatchResult {
	if !cfg.Diarization.RememberSpeakers || len(centroids) == 0 {
		return nil
	}
	modelHash, err := s.ModelHash(cfg.Diarization.EmbeddingModel)
	if err != nil {
		s.logger.Printf("voice match: cannot hash embedding model: %v", err)
		return nil
	}
	people, err := s.store.People()
	if err != nil {
		s.logger.Printf("voice match: cannot list people: %v", err)
		return nil
	}
	if len(people) == 0 {
		return nil
	}
	all, err := s.store.AllVoiceSamples()
	if err != nil {
		s.logger.Printf("voice match: cannot list samples: %v", err)
		return nil
	}
	byPerson := make(map[string][]model.VoiceSample)
	for _, sample := range all {
		if sample.ModelSHA256 != modelHash {
			continue
		}
		byPerson[sample.PersonID] = append(byPerson[sample.PersonID], sample)
	}

	perSpeaker := make(map[string][]profileCandidate, len(centroids))
	for speaker, centroid := range centroids {
		list := make([]profileCandidate, 0, len(people))
		for _, person := range people {
			samples := byPerson[person.ID]
			if len(samples) == 0 {
				continue
			}
			score := sampleSetScore(centroid, samples)
			list = append(list, profileCandidate{person: person, score: score, count: len(samples), meetings: distinctMeetings(samples)})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
		if len(list) > 0 {
			perSpeaker[speaker] = list
		}
	}
	if len(perSpeaker) == 0 {
		return nil
	}
	// Speakers with the strongest profile claim are assigned automatic names
	// first. Two clusters of one meeting must not receive the same person
	// automatically; the second one becomes a suggestion.
	speakers := make([]string, 0, len(perSpeaker))
	for speaker := range perSpeaker {
		speakers = append(speakers, speaker)
	}
	sort.Slice(speakers, func(i, j int) bool {
		return perSpeaker[speakers[i]][0].score > perSpeaker[speakers[j]][0].score
	})
	used := make(map[string]bool)
	result := make(map[string]MatchResult, len(perSpeaker))
	for _, speaker := range speakers {
		list := perSpeaker[speaker]
		top := list[0]
		suggestions := make([]model.SpeakerMatch, 0, 3)
		for _, item := range list {
			if len(suggestions) >= 3 || item.score < cfg.Diarization.VoiceSuggestThreshold {
				break
			}
			suggestions = append(suggestions, model.SpeakerMatch{PersonID: item.person.ID, Name: item.person.Name, Score: round(item.score), SampleCount: item.count})
		}
		match := MatchResult{Score: top.score, Candidates: suggestions}
		if !used[top.person.ID] &&
			top.score >= cfg.Diarization.VoiceAutoThreshold &&
			top.count >= MaturitySamples &&
			top.meetings >= MaturityMeetings &&
			scoreMargin(list) >= AutoMargin {
			match.PersonID = top.person.ID
			match.Name = top.person.Name
			match.Score = top.score
			match.Auto = true
			used[top.person.ID] = true
		}
		if match.Auto || len(suggestions) > 0 {
			result[speaker] = match
		}
	}
	return result
}

// Centroids computes a per-speaker centroid from the clean intervals of a
// meeting. It is used by the recognition stage when an older diarization
// artifact has no stored centroids but the WAV is still available.
func (s *Service) Centroids(ctx context.Context, cfg config.Config, audioPath string, speakerIDs []string, candidates []model.VoiceCandidate, turns []model.SpeakerTurn) (map[string][]float32, error) {
	if strings.TrimSpace(audioPath) == "" {
		return nil, ErrAudioUnavailable
	}
	if _, err := os.Stat(audioPath); err != nil {
		return nil, ErrAudioUnavailable
	}
	result := make(map[string][]float32, len(speakerIDs))
	for _, speakerID := range speakerIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected := selectCandidates(candidates, turns, speakerID)
		if len(selected) == 0 {
			continue
		}
		var sum []float32
		count := 0
		for _, candidate := range selected {
			if count >= 3 {
				break
			}
			tempPath, vector, quality, err := s.embedCandidate(ctx, cfg, audioPath, candidate)
			if err != nil {
				continue
			}
			_ = os.Remove(tempPath)
			if !qualityAcceptable(quality) {
				continue
			}
			if sum == nil {
				sum = make([]float32, len(vector))
			}
			for index, value := range vector {
				if index < len(sum) {
					sum[index] += value
				}
			}
			count++
		}
		if count == 0 {
			continue
		}
		normalizeVector(sum)
		result[speakerID] = sum
	}
	if len(result) == 0 {
		return nil, errors.New("no usable speaker embeddings")
	}
	return result, nil
}

// Enroll extracts clean samples for one manually named speaker, resolves the
// profile and stores the vectors and clips. It is idempotent per meeting,
// speaker and interval.
func (s *Service) Enroll(ctx context.Context, cfg config.Config, req EnrollRequest) (EnrollResult, error) {
	var result EnrollResult
	name := strings.TrimSpace(req.SpeakerName)
	if name == "" {
		return result, errors.New("speaker name is empty")
	}
	if !cfg.Diarization.RememberSpeakers {
		return result, nil
	}
	if strings.TrimSpace(req.AudioPath) == "" {
		return result, ErrAudioUnavailable
	}
	if info, err := os.Stat(req.AudioPath); err != nil || info.Size() <= 44 {
		return result, ErrAudioUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	candidates := selectCandidates(req.Candidates, req.Turns, req.SpeakerID)
	if len(candidates) == 0 {
		result.Warnings = append(result.Warnings, WarnNoCleanSamples)
		return result, nil
	}

	var prepared []preparedSample
	defer func() {
		for _, item := range prepared {
			if item.tempPath != "" {
				_ = os.Remove(item.tempPath)
			}
		}
	}()
	for _, candidate := range candidates {
		if len(prepared) >= MaxSamplesPerMeeting {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		tempPath, vector, quality, err := s.embedCandidate(ctx, cfg, req.AudioPath, candidate)
		if err != nil {
			s.logger.Printf("voice enroll: sample rejected meeting=%s speaker=%s error=%v", req.MeetingUID, req.SpeakerID, err)
			result.Warnings = append(result.Warnings, WarnSomeSamplesRejected)
			continue
		}
		if !qualityAcceptable(quality) {
			_ = os.Remove(tempPath)
			s.logger.Printf("voice enroll: quality rejected meeting=%s speaker=%s duration=%d rms=%.4f speech=%.2f clipped=%.3f", req.MeetingUID, req.SpeakerID, quality.DurationMS, quality.RMS, quality.SpeechRatio, quality.Clipped)
			result.Warnings = append(result.Warnings, WarnSomeSamplesRejected)
			continue
		}
		prepared = append(prepared, preparedSample{candidate: candidate, vector: vector, quality: quality, tempPath: tempPath})
	}
	if len(prepared) == 0 {
		result.Warnings = append(result.Warnings, WarnNoCleanSamples)
		return result, nil
	}
	// A cluster that actually contains two people yields samples with two
	// distinct voices. Refusing to enroll is safer than poisoning the profile
	// and every future automatic match.
	if !cohesiveSamples(prepared) {
		result.Warnings = append(result.Warnings, WarnInconsistentSamples)
		return result, nil
	}
	modelHash, err := s.ModelHash(cfg.Diarization.EmbeddingModel)
	if err != nil {
		return result, fmt.Errorf("hash embedding model: %w", err)
	}
	person, created, resolveWarnings, err := s.resolvePerson(req, prepared, modelHash)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, resolveWarnings...)
	existing, err := s.store.VoiceSamples(person.ID)
	if err != nil {
		return result, err
	}
	var replace *model.VoiceSample
	for _, item := range prepared {
		if duplicateVector(item.vector, existing, modelHash) {
			continue
		}
		if len(existing) >= MaxSamplesPerPerson {
			worst := worstSample(existing)
			if worst == nil || item.quality.Score <= worst.Quality.Score+0.05 {
				result.Warnings = append(result.Warnings, WarnAlreadyEnoughSamples)
				break
			}
			// Copy the value: appending to existing may reallocate and the
			// pointer would refer to the old backing array.
			replaced := *worst
			replace = &replaced
		}
		sampleID := uuidv7.New()
		clipPath := filepath.Join(cfg.App.DataDir, "voices", person.ID, sampleID+".opus")
		if err := os.MkdirAll(filepath.Dir(clipPath), 0o700); err != nil {
			result.Warnings = append(result.Warnings, WarnSomeSamplesRejected)
			s.logger.Printf("voice enroll: create clip directory: %v", err)
			continue
		}
		if err := audio.EncodeWAVToOggOpus(item.tempPath, clipPath, req.MeetingUID, cfg.Storage.OpusBitrateKbps); err != nil {
			result.Warnings = append(result.Warnings, WarnSomeSamplesRejected)
			s.logger.Printf("voice enroll: encode clip: %v", err)
			continue
		}
		clipHash, _ := fileHash(clipPath)
		sample := model.VoiceSample{
			ID:          sampleID,
			PersonID:    person.ID,
			MeetingUID:  req.MeetingUID,
			SpeakerID:   req.SpeakerID,
			SegmentID:   segmentFor(req.Segments, item.candidate),
			Source:      req.Source,
			StartMS:     item.candidate.StartMS,
			EndMS:       item.candidate.EndMS,
			Vector:      item.vector,
			Dim:         len(item.vector),
			ModelSHA256: modelHash,
			ModelName:   filepath.Base(cfg.Diarization.EmbeddingModel),
			Quality:     item.quality,
			ClipPath:    clipPath,
			ClipSHA256:  clipHash,
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.store.SaveVoiceSample(sample); err != nil {
			_ = os.Remove(clipPath)
			result.Warnings = append(result.Warnings, WarnSomeSamplesRejected)
			s.logger.Printf("voice enroll: save sample: %v", err)
			continue
		}
		existing = append(existing, sample)
		result.Added++
		if replace != nil {
			_ = s.store.DeleteVoiceSample(person.ID, replace.ID)
			if replace.ClipPath != "" {
				_ = os.Remove(replace.ClipPath)
			}
			result.Replaced++
			existing = removeSample(existing, replace.ID)
			replace = nil
		}
	}
	result.PersonID = person.ID
	result.PersonName = person.Name
	if result.Added == 0 && created {
		_ = s.store.DeletePerson(person.ID)
		result.PersonID = ""
		result.PersonName = ""
	}
	if result.Added > 0 || (!created && person.ID != "") {
		person.UpdatedAt = time.Now().UTC()
		_ = s.store.SavePerson(person)
	}
	return result, nil
}

func (s *Service) embedCandidate(ctx context.Context, cfg config.Config, audioPath string, candidate model.VoiceCandidate) (string, []float32, model.VoiceQuality, error) {
	start, end := candidate.StartMS, candidate.EndMS
	if start < 0 {
		start = 0
	}
	if end-start > maxSampleMS {
		end = start + maxSampleMS
	}
	if end-start < minSampleMS {
		return "", nil, model.VoiceQuality{}, errors.New("sample is shorter than 1.5 s")
	}
	temp, err := os.CreateTemp("", "lma-voice-*.wav")
	if err != nil {
		return "", nil, model.VoiceQuality{}, err
	}
	tempPath := temp.Name()
	_ = temp.Close()
	if err := audio.SlicePCM16WAV(audioPath, tempPath, start, end); err != nil {
		_ = os.Remove(tempPath)
		return "", nil, model.VoiceQuality{}, err
	}
	samples, err := inference.LoadWAV16kMono(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", nil, model.VoiceQuality{}, err
	}
	rms, speech, clipped := voiceMetrics(samples)
	duration := end - start
	quality := model.VoiceQuality{
		DurationMS:  duration,
		RMS:         rms,
		SpeechRatio: speech,
		Clipped:     clipped,
	}
	quality.Score = qualityScore(candidate.Score, quality)
	vector, err := s.embed(ctx, cfg, samples)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", nil, model.VoiceQuality{}, err
	}
	return tempPath, vector, quality, nil
}

func (s *Service) embed(ctx context.Context, cfg config.Config, samples []float32) ([]float32, error) {
	if len(samples) < 16000 {
		return nil, errors.New("sample is too short for an embedding")
	}
	features, frames := inference.WeSpeakerFeatures(samples)
	if frames <= 0 {
		return nil, errors.New("no speaker features")
	}
	runtimePath := modelmanager.ResolveRuntimeLibrary(cfg.Inference.RuntimePath, cfg.Inference.RuntimeVersion)
	if override := strings.TrimSpace(os.Getenv("LOCALMEETASSIST_ONNXRUNTIME_PATH")); override != "" {
		runtimePath = override
	}
	if err := inference.EnsureRuntime(runtimePath); err != nil {
		return nil, err
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.session == nil || s.sessionModel != cfg.Diarization.EmbeddingModel {
		if s.session != nil {
			s.session.Destroy()
			s.session = nil
		}
		session, err := ort.NewDynamicAdvancedSession(cfg.Diarization.EmbeddingModel, []string{"feats"}, []string{"embs"}, nil)
		if err != nil {
			return nil, err
		}
		s.session = session
		s.sessionModel = cfg.Diarization.EmbeddingModel
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := ort.NewTensor(ort.Shape{1, int64(frames), 80}, features)
	if err != nil {
		return nil, err
	}
	defer input.Destroy()
	outputs := []ort.Value{nil}
	if err := s.session.Run([]ort.Value{input}, outputs); err != nil {
		return nil, err
	}
	defer func() {
		for _, value := range outputs {
			if value != nil {
				value.Destroy()
			}
		}
	}()
	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, errors.New("unexpected WeSpeaker output")
	}
	vector := append([]float32(nil), tensor.GetData()...)
	normalizeVector(vector)
	return vector, nil
}

func (s *Service) resolvePerson(req EnrollRequest, prepared []preparedSample, modelHash string) (model.Person, bool, []string, error) {
	vectors := make([][]float32, 0, len(prepared))
	for _, item := range prepared {
		vectors = append(vectors, item.vector)
	}
	name := strings.TrimSpace(req.SpeakerName)
	if req.ExplicitPersonID != "" {
		if person, err := s.store.Person(req.ExplicitPersonID); err == nil {
			return person, false, nil, nil
		}
	}
	people, err := s.store.People()
	if err != nil {
		return model.Person{}, false, nil, err
	}
	find := func(id string) (model.Person, bool) {
		for _, person := range people {
			if person.ID == id {
				return person, true
			}
		}
		return model.Person{}, false
	}
	if req.ExistingPersonID != "" {
		if person, ok := find(req.ExistingPersonID); ok {
			if strings.EqualFold(strings.TrimSpace(person.Name), name) {
				return person, false, nil, nil
			}
			if score, count := s.personScore(person.ID, vectors, modelHash); count == 0 || score >= linkThreshold {
				// The voice is the same but the user refined the name. Renaming
				// the linked profile is safer than creating a duplicate.
				person.Name = name
				person.NeedsQualifier = false
				person.UpdatedAt = time.Now().UTC()
				if err := s.store.SavePerson(person); err != nil {
					return model.Person{}, false, nil, err
				}
				return person, false, nil, nil
			}
		}
	}
	var sameName []model.Person
	for _, person := range people {
		if strings.EqualFold(strings.TrimSpace(person.Name), name) {
			sameName = append(sameName, person)
		}
	}
	if len(sameName) == 0 {
		person, err := s.createPerson(name, false)
		return person, true, nil, err
	}
	best := model.Person{}
	bestScore := -1.0
	bestCount := 0
	withoutSamples := model.Person{}
	for _, person := range sameName {
		score, count := s.personScore(person.ID, vectors, modelHash)
		if count == 0 {
			if withoutSamples.ID == "" {
				withoutSamples = person
			}
			continue
		}
		if score > bestScore {
			best, bestScore, bestCount = person, score, count
		}
	}
	if bestCount > 0 && bestScore >= linkThreshold {
		return best, false, nil, nil
	}
	if bestCount == 0 && withoutSamples.ID != "" {
		return withoutSamples, false, nil, nil
	}
	// Same name, different voice: a namesake. The unique ID keeps them apart and
	// the UI asks for a qualifier.
	person, err := s.createPerson(name, true)
	if err != nil {
		return model.Person{}, false, nil, err
	}
	return person, true, []string{WarnProfileNeedsQualifier}, nil
}

func (s *Service) createPerson(name string, namesake bool) (model.Person, error) {
	now := time.Now().UTC()
	person := model.Person{ID: uuidv7.New(), Name: name, NeedsQualifier: namesake, CreatedAt: now, UpdatedAt: now}
	if err := s.store.SavePerson(person); err != nil {
		return model.Person{}, err
	}
	return person, nil
}

func (s *Service) personScore(personID string, vectors [][]float32, modelHash string) (float64, int) {
	samples, err := s.store.VoiceSamples(personID)
	if err != nil {
		return -1, 0
	}
	scores := make([]float64, 0, len(samples)*len(vectors))
	for _, sample := range samples {
		if sample.ModelSHA256 != modelHash || len(sample.Vector) == 0 {
			continue
		}
		for _, vector := range vectors {
			scores = append(scores, cosine(vector, sample.Vector))
		}
	}
	if len(scores) == 0 {
		return -1, 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	top := min(3, len(scores))
	var sum float64
	for _, score := range scores[:top] {
		sum += score
	}
	return sum / float64(top), len(scores)
}

func selectCandidates(candidates []model.VoiceCandidate, turns []model.SpeakerTurn, speakerID string) []model.VoiceCandidate {
	selected := make([]model.VoiceCandidate, 0, MaxSamplesPerMeeting)
	for _, candidate := range candidates {
		if candidate.SpeakerID != speakerID {
			continue
		}
		if candidate.EndMS-candidate.StartMS < minSampleMS {
			continue
		}
		selected = append(selected, candidate)
	}
	if len(selected) == 0 {
		selected = candidatesFromTurns(turns, speakerID)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Score > selected[j].Score })
	unique := make([]model.VoiceCandidate, 0, len(selected))
	for _, candidate := range selected {
		overlapped := false
		for _, kept := range unique {
			start := max(candidate.StartMS, kept.StartMS)
			end := min(candidate.EndMS, kept.EndMS)
			shortest := min(candidate.EndMS-candidate.StartMS, kept.EndMS-kept.StartMS)
			if shortest > 0 && end-start > shortest/2 {
				overlapped = true
				break
			}
		}
		if !overlapped {
			unique = append(unique, candidate)
		}
	}
	if len(unique) > 8 {
		unique = unique[:8]
	}
	return unique
}

// candidatesFromTurns is the fallback for meetings processed before clean
// candidates were stored. It subtracts the turns of other speakers.
func candidatesFromTurns(turns []model.SpeakerTurn, speakerID string) []model.VoiceCandidate {
	result := make([]model.VoiceCandidate, 0)
	for _, turn := range turns {
		if turn.SpeakerID != speakerID || turn.EndMS-turn.StartMS < minSampleMS {
			continue
		}
		intervals := [][2]int64{{turn.StartMS, turn.EndMS}}
		for _, other := range turns {
			if other.SpeakerID == speakerID || other.EndMS <= turn.StartMS || other.StartMS >= turn.EndMS {
				continue
			}
			intervals = subtractInterval(intervals, other.StartMS, other.EndMS)
		}
		for _, interval := range intervals {
			duration := interval[1] - interval[0]
			if duration < minSampleMS {
				continue
			}
			if duration > maxSampleMS {
				interval[1] = interval[0] + maxSampleMS
				duration = maxSampleMS
			}
			result = append(result, model.VoiceCandidate{
				SpeakerID: speakerID,
				StartMS:   interval[0],
				EndMS:     interval[1],
				Score:     math.Min(1, float64(duration)/6000),
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	return result
}

func subtractInterval(intervals [][2]int64, start, end int64) [][2]int64 {
	out := make([][2]int64, 0, len(intervals)+1)
	for _, interval := range intervals {
		if end <= interval[0] || start >= interval[1] {
			out = append(out, interval)
			continue
		}
		if start > interval[0] {
			out = append(out, [2]int64{interval[0], start})
		}
		if end < interval[1] {
			out = append(out, [2]int64{end, interval[1]})
		}
	}
	return out
}

func voiceMetrics(samples []float32) (rms, speechRatio, clipped float64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	var sum float64
	clippedCount := 0
	for _, value := range samples {
		sum += float64(value * value)
		if value >= 0.999 || value <= -0.999 {
			clippedCount++
		}
	}
	rms = math.Sqrt(sum / float64(len(samples)))
	const frame = 320 // 20 ms at 16 kHz
	frames, speech := 0, 0
	for start := 0; start+frame <= len(samples); start += frame {
		var frameSum float64
		for _, value := range samples[start : start+frame] {
			frameSum += float64(value * value)
		}
		if math.Sqrt(frameSum/float64(frame)) >= 0.002 {
			speech++
		}
		frames++
	}
	if frames > 0 {
		speechRatio = float64(speech) / float64(frames)
	}
	clipped = float64(clippedCount) / float64(len(samples))
	return rms, speechRatio, clipped
}

func qualityScore(candidateScore float64, quality model.VoiceQuality) float64 {
	duration := math.Min(1, float64(quality.DurationMS)/6000)
	rms := math.Min(1, quality.RMS/0.03)
	speech := math.Min(1, quality.SpeechRatio/0.8)
	base := candidateScore
	if base <= 0 {
		base = duration
	}
	score := 0.4*base + 0.3*duration + 0.2*speech + 0.1*rms
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func qualityAcceptable(quality model.VoiceQuality) bool {
	return quality.DurationMS >= minSampleMS &&
		quality.RMS >= minRMS &&
		quality.SpeechRatio >= minSpeechRatio &&
		quality.Clipped <= maxClipped
}

// cohesiveSamples checks that the selected intervals really sound like one
// person. Same-speaker windows usually score well above 0.5; a merged cluster
// produces pairs far below that.
func cohesiveSamples(samples []preparedSample) bool {
	if len(samples) < 2 {
		return true
	}
	for left := 0; left < len(samples); left++ {
		for right := left + 1; right < len(samples); right++ {
			if cosine(samples[left].vector, samples[right].vector) < 0.35 {
				return false
			}
		}
	}
	return true
}

func duplicateVector(vector []float32, samples []model.VoiceSample, modelHash string) bool {
	for _, sample := range samples {
		if sample.ModelSHA256 != modelHash || len(sample.Vector) == 0 {
			continue
		}
		if cosine(vector, sample.Vector) >= duplicateThreshold {
			return true
		}
	}
	return false
}

func worstSample(samples []model.VoiceSample) *model.VoiceSample {
	if len(samples) == 0 {
		return nil
	}
	worst := &samples[0]
	for index := range samples {
		if samples[index].Quality.Score < worst.Quality.Score {
			worst = &samples[index]
		}
	}
	return worst
}

func removeSample(samples []model.VoiceSample, id string) []model.VoiceSample {
	out := samples[:0]
	for _, sample := range samples {
		if sample.ID != id {
			out = append(out, sample)
		}
	}
	return out
}

func segmentFor(segments []model.Segment, candidate model.VoiceCandidate) string {
	bestID, bestOverlap := "", int64(0)
	for _, segment := range segments {
		start := max(segment.StartMS, candidate.StartMS)
		end := min(segment.EndMS, candidate.EndMS)
		if overlap := end - start; overlap > bestOverlap {
			bestOverlap, bestID = overlap, segment.ID
		}
	}
	return bestID
}

func sampleSetScore(vector []float32, samples []model.VoiceSample) float64 {
	scores := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if len(sample.Vector) == 0 {
			continue
		}
		scores = append(scores, cosine(vector, sample.Vector))
	}
	if len(scores) == 0 {
		return -1
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	top := min(3, len(scores))
	var sum float64
	for _, score := range scores[:top] {
		sum += score
	}
	return sum / float64(top)
}

func distinctMeetings(samples []model.VoiceSample) int {
	seen := make(map[string]bool)
	for _, sample := range samples {
		if sample.MeetingUID != "" {
			seen[sample.MeetingUID] = true
		}
	}
	return len(seen)
}

func scoreMargin(list []profileCandidate) float64 {
	if len(list) < 2 {
		return 1
	}
	return list[0].score - list[1].score
}

func cosine(a, b []float32) float64 {
	var value float64
	for i := 0; i < min(len(a), len(b)); i++ {
		value += float64(a[i] * b[i])
	}
	return value
}

func normalizeVector(vector []float32) {
	var sum float64
	for _, value := range vector {
		sum += float64(value * value)
	}
	norm := float32(math.Sqrt(sum))
	if norm < 1e-8 {
		return
	}
	for i := range vector {
		vector[i] /= norm
	}
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func round(value float64) float64 {
	return math.Round(value*1000) / 1000
}
