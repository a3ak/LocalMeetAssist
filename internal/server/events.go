package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"localmeetassist/internal/model"
)

// publishEvent pushes an SSE message to every connected client.
func (s *Server) publishEvent(name string, payload any) {
	data, err := json.Marshal(map[string]any{"type": name, "payload": payload})
	if err != nil {
		return
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	for subscriber := range s.events {
		select {
		case subscriber <- data:
		default:
		}
	}
}

// eventStream serves the server-sent events endpoint.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "event streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	subscriber := make(chan []byte, 8)
	s.eventsMu.Lock()
	s.events[subscriber] = struct{}{}
	s.eventsMu.Unlock()
	defer func() {
		s.eventsMu.Lock()
		delete(s.events, subscriber)
		s.eventsMu.Unlock()
	}()
	active, _ := s.activeRecording()
	initial, _ := json.Marshal(map[string]any{"type": "recording", "payload": recordingState(active)})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", initial)
	flusher.Flush()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-subscriber:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// recordingState builds the recording payload sent to the UI.
func recordingState(meeting *model.Meeting) map[string]any {
	if meeting == nil {
		return map[string]any{"active": false, "meeting": nil}
	}
	return map[string]any{"active": true, "meeting": meeting}
}

// activeRecording returns the meeting that is starting or recording.
func (s *Server) activeRecording() (*model.Meeting, error) {
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		return nil, err
	}
	for index := range meetings {
		if meetings[index].Status == "recording" || meetings[index].Status == "starting" {
			meeting := meetings[index]
			return &meeting, nil
		}
	}
	return nil, nil
}
