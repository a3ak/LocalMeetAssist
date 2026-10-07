package server

import (
	"sync"
	"time"
)

// commandTTL bounds how long an integration command result stays cached. The
// specification requires at least five minutes, so a retry after a lost ack can
// never execute the same action twice.
const commandTTL = 10 * time.Minute

// recordingTracker holds the global recording flag together with the revision
// counter that is published to browser clients. The pair is persisted, so a
// restart can report the transition that happened while the process was gone.
type recordingTracker struct {
	mu       sync.Mutex
	active   bool
	revision int64
}

// normaliseRecordingState reconciles the persisted pair with the fact that an
// audio recording cannot survive a process restart.
func (s *Server) normaliseRecordingState() {
	revision, storedActive, err := s.store.RecordingState()
	if err != nil {
		s.logger.Printf("integration state: %v", err)
		return
	}
	if storedActive {
		// The previous process disappeared while recording. That is a real
		// true→false transition which plugins must be able to observe, so the
		// revision grows even though the new process never saw the recording.
		revision++
		if err := s.store.SaveRecordingState(revision, false); err != nil {
			s.logger.Printf("integration state: %v", err)
		}
	}
	s.recording.mu.Lock()
	s.recording.active = false
	s.recording.revision = revision
	s.recording.mu.Unlock()
}

// recordingValues returns the cached activity flag and revision. It never
// mutates them, so `state` and `ask` always agree with what was published.
func (s *Server) recordingValues() (bool, int64) {
	s.recording.mu.Lock()
	defer s.recording.mu.Unlock()
	return s.recording.active, s.recording.revision
}

// syncRecording publishes the global recording state. The revision grows only
// on a real false↔true transition, and the pair is persisted before anything is
// broadcast, so a crash can neither lose nor double-count the change. It is the
// single hook shared by the application buttons and the browser commands.
func (s *Server) syncRecording() {
	active := s.recorder.AnyActive()
	tr := s.recording
	tr.mu.Lock()
	changed := active != tr.active
	if changed {
		tr.active = active
		tr.revision++
	}
	revision := tr.revision
	tr.mu.Unlock()
	if !changed {
		return
	}
	if err := s.store.SaveRecordingState(revision, active); err != nil {
		s.logger.Printf("integration state: %v", err)
	}
	s.browserOnRecordingState(active)
}
