// Package model defines the domain types shared by storage, the pipeline
// and the HTTP API.
package model

import "time"

// Device identifies an audio device by a stable ID and a readable name.
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Meeting is the central record; its UID links recordings, transcripts,
// speakers and artifacts.
type Meeting struct {
	UID             string            `json:"uid"`
	Title           string            `json:"title"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	DurationMS      int64             `json:"duration_ms"`
	Status          string            `json:"status"`
	Source          string            `json:"source"`
	InputDevice     Device            `json:"input_device"`
	OutputDevice    Device            `json:"output_device"`
	SpeakerCount    int               `json:"speaker_count"`
	SummaryStatus   string            `json:"summary_status"`
	Transcript      string            `json:"transcript,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	ArtifactUIDs    []string          `json:"artifact_uids,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ProcessingStage string            `json:"processing_stage,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// Artifact describes one managed file on disk (audio, transcript, summary,
// metadata) together with its integrity hash.
type Artifact struct {
	UID        string    `json:"uid"`
	MeetingUID string    `json:"meeting_uid"`
	Type       string    `json:"type"`
	Path       string    `json:"path"`
	MIMEType   string    `json:"mime_type"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

// Speaker is one diarized voice inside a meeting.
type Speaker struct {
	MeetingUID  string   `json:"meeting_uid"`
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Source      string   `json:"source"`
	Samples     []Sample `json:"samples,omitempty"`
	Fragments   []Sample `json:"fragments,omitempty"`
	// PersonID links the meeting speaker to a known voice profile. It stays
	// empty for SPEAKER_NN entries and for people whose voice was not enrolled.
	PersonID string `json:"person_id,omitempty"`
	// MatchScore is the confidence of the profile match. NameSource is "auto"
	// when the pipeline filled the name and "manual" after a user action.
	MatchScore      float64        `json:"match_score,omitempty"`
	MatchCandidates []SpeakerMatch `json:"match_candidates,omitempty"`
	NameSource      string         `json:"name_source,omitempty"`
}

// SpeakerMatch is one profile suggestion for a meeting speaker.
type SpeakerMatch struct {
	PersonID    string  `json:"person_id"`
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	SampleCount int     `json:"sample_count"`
}

// Person is a known speaker shared by meetings. The unique ID, not the name,
// distinguishes namesakes.
type Person struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	NeedsQualifier bool      `json:"needs_qualifier,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VoiceQuality records why a voice sample was accepted or rejected.
type VoiceQuality struct {
	DurationMS  int64   `json:"duration_ms"`
	RMS         float64 `json:"rms"`
	SpeechRatio float64 `json:"speech_ratio"`
	Clipped     float64 `json:"clipped"`
	Score       float64 `json:"score"`
	Overlap     bool    `json:"overlap"`
}

// VoiceSample is one enrolled voiceprint together with a short reference clip.
type VoiceSample struct {
	ID          string       `json:"id"`
	PersonID    string       `json:"person_id"`
	MeetingUID  string       `json:"meeting_uid,omitempty"`
	SpeakerID   string       `json:"speaker_id,omitempty"`
	SegmentID   string       `json:"segment_id,omitempty"`
	Source      string       `json:"source,omitempty"`
	StartMS     int64        `json:"start_ms"`
	EndMS       int64        `json:"end_ms"`
	Vector      []float32    `json:"vector"`
	Dim         int          `json:"dim"`
	ModelSHA256 string       `json:"model_sha256"`
	Quality     VoiceQuality `json:"quality"`
	ClipPath    string       `json:"clip_path,omitempty"`
	ClipSHA256  string       `json:"clip_sha256,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

// VoiceSampleView is the public representation served to the UI. It hides the
// embedding vector, which is only used by matching.
type VoiceSampleView struct {
	ID         string       `json:"id"`
	MeetingUID string       `json:"meeting_uid,omitempty"`
	SpeakerID  string       `json:"speaker_id,omitempty"`
	Source     string       `json:"source,omitempty"`
	StartMS    int64        `json:"start_ms"`
	EndMS      int64        `json:"end_ms"`
	Quality    VoiceQuality `json:"quality"`
	ClipURL    string       `json:"clip_url,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

// SpeakerTurn is one diarized speech interval assigned to a speaker.
type SpeakerTurn struct {
	StartMS   int64  `json:"start_ms"`
	EndMS     int64  `json:"end_ms"`
	SpeakerID string `json:"speaker_id"`
}

// VoiceCandidate is a clean continuous interval that can be enrolled as a
// voice sample. It contains no audio or biometric data, only time codes.
type VoiceCandidate struct {
	SpeakerID string  `json:"speaker_id"`
	StartMS   int64   `json:"start_ms"`
	EndMS     int64   `json:"end_ms"`
	Score     float64 `json:"score"`
}

// PersonView is a known speaker with public sample metadata.
type PersonView struct {
	Person
	Samples      []VoiceSampleView `json:"samples"`
	MeetingCount int               `json:"meeting_count"`
	LastSeen     *time.Time        `json:"last_seen,omitempty"`
}

// Sample is a time range used as a voice example or as a speaker fragment.
type Sample struct {
	ID      string `json:"id,omitempty"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Source  string `json:"source,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Segment is one transcript line with its speaker and source stream.
type Segment struct {
	ID         string  `json:"id,omitempty"`
	StartMS    int64   `json:"start_ms"`
	EndMS      int64   `json:"end_ms"`
	SpeakerID  string  `json:"speaker_id"`
	Source     string  `json:"source"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence,omitempty"`
}

// Job tracks the status and progress of one processing stage.
type Job struct {
	UID        string    `json:"uid"`
	MeetingUID string    `json:"meeting_uid"`
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts"`
	LastError  string    `json:"last_error,omitempty"`
	Progress   int       `json:"progress,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RecordingStart is the request body for starting a native recording.
type RecordingStart struct {
	Title            string `json:"title"`
	InputDevice      Device `json:"input_device"`
	OutputDevice     Device `json:"output_device"`
	ParticipantCount int    `json:"participant_count,omitempty"`
}

// MeetingPatch carries the optional user edits of a meeting.
type MeetingPatch struct {
	Title            *string `json:"title,omitempty"`
	Transcript       *string `json:"transcript,omitempty"`
	Summary          *string `json:"summary,omitempty"`
	ParticipantCount *int    `json:"participant_count,omitempty"`
}

// Token is a stored API access token. The secret itself is never persisted:
// only its SHA-256 hash and a short fingerprint for display are kept.
type Token struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Hash        string     `json:"hash"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}
