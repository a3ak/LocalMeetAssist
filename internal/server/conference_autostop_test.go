package server

import (
	"context"
	"testing"
	"time"

	"localmeetassist/internal/model"
	"localmeetassist/internal/nativeaudio"
)

// staticActivity feeds the detector a fixed activity snapshot.
type staticActivity struct{ value nativeaudio.Activity }

func (s staticActivity) Activity() nativeaudio.Activity { return s.value }

// TestConferenceAutoStopAfterIdle ends a detection-started recording when the
// microphone and the system audio are idle for the configured delay.
func TestConferenceAutoStopAfterIdle(t *testing.T) {
	s := newBrowserServer(t, "auto")
	s.activity = staticActivity{value: nativeaudio.Activity{Available: true, Microphone: true, System: true}}

	detection := s.config().ConferenceDetection
	detection.StopAfterIdleSeconds = 30

	if _, err := s.startMeetingRecording(context.Background(), model.RecordingStart{}, "detected", nil); err != nil {
		t.Fatal(err)
	}
	s.conference.mu.Lock()
	s.conference.detected = true
	s.conference.recordingApp = "Zoom"
	s.conference.mu.Unlock()

	start := time.Now()
	// Пока приложение использует микрофон и звук, запись продолжается.
	s.observeDetectedRecording(start, detection)
	s.activity = staticActivity{value: nativeaudio.Activity{Available: true}}
	s.observeDetectedRecording(start.Add(time.Second), detection)
	s.observeDetectedRecording(start.Add(20*time.Second), detection)
	if !s.recorder.AnyActive() {
		t.Fatal("the recording was stopped before the delay expired")
	}
	// После задержки детектор останавливает запись сам.
	s.observeDetectedRecording(start.Add(40*time.Second), detection)
	if s.recorder.AnyActive() {
		t.Fatal("the recording was not stopped after the idle delay")
	}
}

// TestConferenceAutoStopDisabled proves the zero delay keeps the recording.
func TestConferenceAutoStopDisabled(t *testing.T) {
	s := newBrowserServer(t, "auto")
	s.activity = staticActivity{value: nativeaudio.Activity{Available: true}}
	detection := s.config().ConferenceDetection
	detection.StopAfterIdleSeconds = 0

	if _, err := s.startMeetingRecording(context.Background(), model.RecordingStart{}, "detected", nil); err != nil {
		t.Fatal(err)
	}
	s.conference.mu.Lock()
	s.conference.detected = true
	s.conference.mu.Unlock()

	start := time.Now()
	s.observeDetectedRecording(start, detection)
	s.observeDetectedRecording(start.Add(time.Hour), detection)
	if !s.recorder.AnyActive() {
		t.Fatal("a zero delay must not stop the recording")
	}
}
