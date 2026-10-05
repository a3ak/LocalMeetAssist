package server

import (
	"context"
	"fmt"
	"net/http"
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
	now := time.Now()
	if strings.TrimSpace(req.Title) == "" {
		req.Title = config.Localized(s.config().App.Language, "Встреча ", "Meeting ") + now.Format("02.01.2006 15:04")
	}
	report := s.recorder.Devices(r.Context())
	req.InputDevice = chooseDevice(req.InputDevice, report.Microphones)
	req.OutputDevice = chooseDevice(req.OutputDevice, report.SystemSources)
	cfg := s.config()
	ownerName := s.ownerName()
	metadata := map[string]string{"microphone_owner_name": ownerName}
	participantCount := req.ParticipantCount
	if participantCount < 0 {
		writeError(w, 400, "participant_count must be between 0 and 65")
		return
	}
	if participantCount > 0 {
		if participantCount > 65 {
			writeError(w, 400, "participant_count must be between 1 and 65")
			return
		}
		metadata["participant_count"] = strconv.Itoa(participantCount)
	}
	m := model.Meeting{UID: uuidv7.New(), Title: strings.TrimSpace(req.Title), StartedAt: now, Status: "starting", Source: "recorded", InputDevice: req.InputDevice, OutputDevice: req.OutputDevice, SummaryStatus: "disabled", Metadata: metadata, CreatedAt: now, UpdatedAt: now}
	dir := s.meetingDir(m)
	// Сначала сохраняем встречу: даже ошибка открытия устройства должна быть видна
	// в календаре и диагностике, а не теряться до записи в БД.
	if err := s.store.SaveMeeting(m); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	session, err := s.recorder.Start(m.UID, dir, cfg.Audio.SampleRate, cfg.Audio.Channels, req.InputDevice, req.OutputDevice)
	if err != nil {
		m.Status, m.LastError, m.UpdatedAt = "failed", err.Error(), time.Now()
		_ = s.store.SaveMeeting(m)
		appLogging.Errorf(s.logger, "recording failed uid=%s error=%v", m.UID, err)
		writeError(w, 500, fmt.Sprintf("could not start recording: %v; the meeting was kept in the calendar", err))
		return
	}
	m.Status, m.InputDevice, m.OutputDevice, m.UpdatedAt = "recording", session.InputDevice, session.OutputDevice, time.Now()
	if err := s.store.SaveMeeting(m); err != nil {
		_, _ = s.recorder.Stop(m.UID)
		writeError(w, 500, err.Error())
		return
	}
	s.publishEvent("recording", recordingState(&m))
	writeJSON(w, 201, m)
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
	m, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, 404, "meeting not found")
		return
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
		appLogging.Errorf(s.logger, "recording stop failed uid=%s error=%v", uid, err)
		writeError(w, 500, err.Error())
		return
	}
	m.FinishedAt = &now
	m.DurationMS = now.Sub(session.StartedAt).Milliseconds()
	m.Status = "processing"
	m.ProcessingStage = "queued"
	m.UpdatedAt = now
	if err := s.store.SaveMeeting(m); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.publishEvent("recording", recordingState(nil))
	if err := s.pipeline.Start(uid); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, m)
}

// StopActiveRecording finalizes WAV headers during a controlled application
// shutdown. Without this step an interrupted meeting could contain PCM data
// with an unfinished RIFF header.
func (s *Server) StopActiveRecording(ctx context.Context) error {
	session, err := s.recorder.StopActive()
	if session == nil {
		return err
	}
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
