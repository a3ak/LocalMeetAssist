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
