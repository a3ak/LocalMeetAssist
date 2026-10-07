package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/config"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

// recordings reports the active recording or starts a new one.
func (s *Server) recordings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		meeting, err := s.activeRecording()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, recordingState(meeting))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req model.RecordingStart
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.ParticipantCount < 0 || req.ParticipantCount > 65 {
		writeError(w, 400, "participant_count must be between 0 and 65")
		return
	}
	m, err := s.startMeetingRecording(r.Context(), req, "recorded", nil)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, m)
}

// startMeetingRecording creates a meeting and starts native capture. source is
// the meeting origin ("recorded" or "browser"); extra entries are merged into
// the meeting metadata.
func (s *Server) startMeetingRecording(ctx context.Context, req model.RecordingStart, source string, extra map[string]string) (model.Meeting, error) {
	now := time.Now()
	if strings.TrimSpace(req.Title) == "" {
		req.Title = config.Localized(s.config().App.Language, "Встреча ", "Meeting ") + now.Format("02.01.2006 15:04")
	}
	report := s.recorder.Devices(ctx)
	req.InputDevice = chooseDevice(req.InputDevice, report.Microphones)
	req.OutputDevice = chooseDevice(req.OutputDevice, report.SystemSources)
	cfg := s.config()
	metadata := map[string]string{"microphone_owner_name": s.ownerName()}
	if req.ParticipantCount > 0 {
		metadata["participant_count"] = strconv.Itoa(req.ParticipantCount)
	}
	for k, v := range extra {
		metadata[k] = v
	}
	if source == "" {
		source = "recorded"
	}
	m := model.Meeting{UID: uuidv7.New(), Title: strings.TrimSpace(req.Title), StartedAt: now, Status: "starting", Source: source, InputDevice: req.InputDevice, OutputDevice: req.OutputDevice, SummaryStatus: "disabled", Metadata: metadata, CreatedAt: now, UpdatedAt: now}
	// The meeting is saved before opening devices so a failure is still visible
	// in the calendar instead of being lost before the first database write.
	if err := s.store.SaveMeeting(m); err != nil {
		return model.Meeting{}, err
	}
	session, err := s.recorder.Start(m.UID, s.meetingDir(m), cfg.Audio.SampleRate, cfg.Audio.Channels, req.InputDevice, req.OutputDevice)
	if err != nil {
		m.Status, m.LastError, m.UpdatedAt = "failed", err.Error(), time.Now()
		_ = s.store.SaveMeeting(m)
		appLogging.Errorf(s.logger, "recording failed uid=%s error=%v", m.UID, err)
		return model.Meeting{}, fmt.Errorf("could not start recording: %v; the meeting was kept in the calendar", err)
	}
	m.Status, m.InputDevice, m.OutputDevice, m.UpdatedAt = "recording", session.InputDevice, session.OutputDevice, time.Now()
	if err := s.store.SaveMeeting(m); err != nil {
		_, _ = s.recorder.Stop(m.UID)
		return model.Meeting{}, err
	}
	s.publishEvent("recording", recordingState(&m))
	s.syncRecording()
	return m, nil
}

// recordingPath routes recording stop and status requests.
func (s *Server) recordingPath(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/recordings/"))
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	uid, action := parts[0], parts[1]
	switch action {
	case "chunk":
		writeError(w, http.StatusGone, "browser audio chunk upload was removed; recording is performed by the native backend")
	case "stop":
		s.recordingStop(w, r, uid)
	case "status":
		writeJSON(w, 200, map[string]any{"active": s.recorder.Active(uid)})
	default:
		http.NotFound(w, r)
	}
}

// recordingStop finalizes the recording and starts the pipeline.
func (s *Server) recordingStop(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	m, err := s.stopMeetingRecording(uid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, 404, "meeting not found")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, m)
}

// stopMeetingRecording finalizes the recording and starts the pipeline. It is
// shared by the recording endpoint and the browser integration.
func (s *Server) stopMeetingRecording(uid string) (model.Meeting, error) {
	m, err := s.store.Meeting(uid)
	if err != nil {
		return model.Meeting{}, err
	}
	session, err := s.recorder.Stop(uid)
	now := time.Now()
	if err != nil {
		m.FinishedAt = &now
		if session != nil {
			m.DurationMS = now.Sub(session.StartedAt).Milliseconds()
		}
		m.Status, m.LastError, m.UpdatedAt = "failed", err.Error(), now
		_ = s.store.SaveMeeting(m)
		s.publishEvent("recording", recordingState(nil))
		s.syncRecording()
		appLogging.Errorf(s.logger, "recording stop failed uid=%s error=%v", uid, err)
		return m, err
	}
	m.FinishedAt = &now
	m.DurationMS = now.Sub(session.StartedAt).Milliseconds()
	m.Status = "processing"
	m.ProcessingStage = "queued"
	m.UpdatedAt = now
	if err := s.store.SaveMeeting(m); err != nil {
		return m, err
	}
	s.publishEvent("recording", recordingState(nil))
	s.syncRecording()
	return m, s.pipeline.Start(uid)
}

// StopActiveRecording finalizes WAV headers during a controlled application
// shutdown. Without this step an interrupted meeting could contain PCM data
// with an unfinished RIFF header.
func (s *Server) StopActiveRecording(ctx context.Context) error {
	session, err := s.recorder.StopActive()
	if session == nil {
		return err
	}
	// The recording is over as far as this process is concerned, so the
	// transition is persisted before anything else can fail.
	s.syncRecording()
	meeting, loadErr := s.store.Meeting(session.MeetingUID)
	if loadErr != nil {
		return loadErr
	}
	now := time.Now()
	meeting.FinishedAt = &now
	meeting.DurationMS = now.Sub(session.StartedAt).Milliseconds()
	meeting.UpdatedAt = now
	if err != nil {
		meeting.Status = "failed"
		meeting.LastError = err.Error()
		_ = s.store.SaveMeeting(meeting)
		return err
	}
	meeting.Status = "processing"
	meeting.ProcessingStage = "shutdown_finalization"
	if saveErr := s.store.SaveMeeting(meeting); saveErr != nil {
		return saveErr
	}
	return s.pipeline.Run(ctx, meeting.UID)
}

// chooseDevice resolves the requested device or falls back to the first one.
func chooseDevice(requested model.Device, available []model.Device) model.Device {
	if requested.ID != "" {
		if requested.Name == "" {
			for _, d := range available {
				if d.ID == requested.ID {
					requested.Name = d.Name
					break
				}
			}
		}
		if requested.Name == "" {
			requested.Name = requested.ID
		}
		return requested
	}
	if len(available) > 0 {
		return available[0]
	}
	return requested
}
