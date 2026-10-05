package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

// meetings lists meetings in a range or creates a manual meeting.
func (s *Server) meetings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var from, to *time.Time
		if v := r.URL.Query().Get("from"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				from = &t
			}
		}
		if v := r.URL.Query().Get("to"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				to = &t
			}
		}
		items, err := s.store.ListMeetings(from, to)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, items)
	case http.MethodPost:
		var req struct {
			Title     string `json:"title"`
			StartedAt string `json:"started_at"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		started := time.Now()
		if req.StartedAt != "" {
			t, err := time.Parse(time.RFC3339, req.StartedAt)
			if err != nil {
				writeError(w, 400, "invalid started_at")
				return
			}
			started = t
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = config.Localized(s.config().App.Language, "Встреча ", "Meeting ") + started.Format("02.01.2006 15:04")
		}
		now := time.Now()
		ownerName := s.ownerName()
		m := model.Meeting{UID: uuidv7.New(), Title: req.Title, StartedAt: started, Status: "created", Source: "manual", SummaryStatus: "pending", Metadata: map[string]string{"microphone_owner_name": ownerName}, CreatedAt: now, UpdatedAt: now}
		if err := s.store.SaveMeeting(m); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, m)
	default:
		methodNotAllowed(w)
	}
}

// meetingPath routes /api/v1/meetings/{uid}/... requests.
func (s *Server) meetingPath(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/meetings/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	uid := parts[0]
	if len(parts) == 1 {
		s.meetingCRUD(w, r, uid)
		return
	}
	switch parts[1] {
	case "artifacts":
		if len(parts) == 2 {
			s.artifacts(w, r, uid)
		} else {
			s.artifactDownload(w, r, uid, parts[2])
		}
	case "speakers":
		if len(parts) == 2 {
			s.speakers(w, r, uid)
		} else if parts[2] == "merge" {
			s.mergeSpeakers(w, r, uid)
		} else {
			s.speaker(w, r, uid, parts[2])
		}
	case "jobs":
		if len(parts) == 2 && r.Method == http.MethodGet {
			s.jobStatus(w, uid)
		} else if len(parts) == 3 && parts[2] == "cancel" {
			s.cancelJob(w, r, uid)
		} else {
			s.retryJob(w, r, uid, parts)
		}
	default:
		http.NotFound(w, r)
	}
}

// jobStatus returns the processing state and jobs of one meeting.
func (s *Server) jobStatus(w http.ResponseWriter, uid string) {
	meeting, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found")
		return
	}
	jobs, err := s.store.Jobs(uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":              jobs,
		"processing_active": s.pipeline.IsRunning(uid),
		"status":            meeting.Status,
		"processing_stage":  meeting.ProcessingStage,
		"last_error":        meeting.LastError,
	})
}

// meetingCRUD handles read, patch and delete for one meeting.
func (s *Server) meetingCRUD(w http.ResponseWriter, r *http.Request, uid string) {
	m, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, 404, "meeting not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		arts, _ := s.liveArtifacts(uid)
		speakers, _ := s.store.Speakers(uid)
		jobs, _ := s.store.Jobs(uid)
		writeJSON(w, 200, map[string]any{"meeting": m, "artifacts": arts, "speakers": speakers, "jobs": jobs, "processing_active": s.pipeline.IsRunning(uid)})
	case http.MethodPatch:
		var p model.MeetingPatch
		if err := decodeJSON(r, &p); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if p.Title != nil {
			m.Title = strings.TrimSpace(*p.Title)
		}
		if p.Transcript != nil {
			m.Transcript = *p.Transcript
			if strings.TrimSpace(m.Summary) != "" {
				if m.Metadata == nil {
					m.Metadata = make(map[string]string)
				}
				m.Metadata["summary_stale"] = "true"
			}
		}
		if p.Summary != nil {
			m.Summary = *p.Summary
			m.SummaryStatus = "completed"
			if m.Metadata != nil {
				delete(m.Metadata, "summary_stale")
			}
		}
		participantCount := p.ParticipantCount
		if participantCount != nil {
			if *participantCount < 0 || *participantCount > 65 {
				writeError(w, http.StatusBadRequest, "participant_count must be between 0 and 65")
				return
			}
			if s.pipeline.IsRunning(uid) {
				writeError(w, http.StatusConflict, "the participant count cannot be changed while processing")
				return
			}
			if m.Metadata == nil {
				m.Metadata = make(map[string]string)
			}
			if *participantCount == 0 {
				delete(m.Metadata, "participant_count")
			} else {
				m.Metadata["participant_count"] = strconv.Itoa(*participantCount)
			}
		}
		m.UpdatedAt = time.Now()
		if err := s.store.SaveMeeting(m); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, m)
	case http.MethodDelete:
		arts, err := s.store.DeleteMeeting(uid)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		for _, a := range arts {
			if s.safeManagedPath(a.Path) {
				_ = os.Remove(a.Path)
			}
		}
		dir := s.meetingDir(m)
		if s.safeManagedPath(dir) {
			_ = os.RemoveAll(dir)
		}
		w.WriteHeader(204)
	default:
		methodNotAllowed(w)
	}
}

// retryJob queues a stage retry for a meeting.
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request, uid string, parts []string) {
	if r.Method != http.MethodPost || len(parts) < 4 || parts[3] != "retry" {
		http.NotFound(w, r)
		return
	}
	m, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, 404, "meeting not found")
		return
	}
	if s.pipeline.IsRunning(uid) {
		writeError(w, http.StatusConflict, "processing is already running")
		return
	}
	stage := parts[2]
	switch stage {
	case "all", "transcribing", "diarizing", "summarizing", "encoding":
	default:
		writeError(w, http.StatusBadRequest, "unknown processing stage")
		return
	}
	if stage == "all" || stage == "transcribing" || stage == "encoding" {
		audioDir := filepath.Join(s.meetingDir(m), "audio")
		if !validWAVFile(filepath.Join(audioDir, "microphone.wav")) || !validWAVFile(filepath.Join(audioDir, "system.wav")) {
			writeError(w, http.StatusConflict, "no prepared sources: import a PCM16 WAV as microphone, system audio or mixed")
			return
		}
	}
	m.Status = "processing"
	m.ProcessingStage = "queued"
	m.LastError = ""
	m.UpdatedAt = time.Now()
	if err := s.store.SaveMeeting(m); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.pipeline.StartStage(uid, stage); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "processing", "stage": stage})
}

// cancelJob stops the active pipeline of a meeting.
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := s.store.Meeting(uid); err != nil {
		writeError(w, http.StatusNotFound, "meeting not found")
		return
	}
	if !s.pipeline.Cancel(uid) {
		writeError(w, http.StatusConflict, "no active processing found")
		return
	}
	s.logger.Printf("pipeline cancellation requested uid=%s", uid)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "canceling"})
}
