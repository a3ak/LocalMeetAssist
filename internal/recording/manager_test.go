package recording

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/nativeaudio"
)

type blockingDeviceBackend struct{ syntheticBackend }

func (blockingDeviceBackend) Devices() ([]nativeaudio.Device, []nativeaudio.Device, error) {
	select {}
}

func TestDevicesHonorsContextWhenNativeEnumerationBlocks(t *testing.T) {
	manager := newManager(config.Defaults().Audio, log.New(io.Discard, "", 0), blockingDeviceBackend{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	report := manager.Devices(ctx)
	if time.Since(started) > 250*time.Millisecond {
		t.Fatalf("device enumeration ignored context: %s", time.Since(started))
	}
	if report.Available || len(report.Warnings) == 0 {
		t.Fatalf("expected timeout warning, got %+v", report)
	}
}

type syntheticBackend struct {
	silentSystem bool
	// health is the sequence of system stream states the fake session reports.
	health []nativeaudio.SystemHealth
	// microphoneHealth is the same sequence for the microphone.
	microphoneHealth []nativeaudio.SystemHealth
}

func (syntheticBackend) Name() string { return "synthetic-native-test" }
func (syntheticBackend) Devices() ([]nativeaudio.Device, []nativeaudio.Device, error) {
	return []nativeaudio.Device{{ID: "mic", Name: "Test microphone", IsDefault: true}},
		[]nativeaudio.Device{{ID: "system", Name: "Test system", IsDefault: true}}, nil
}
func (backend syntheticBackend) Start(request nativeaudio.StartRequest) (nativeaudio.Session, error) {
	microphone, err := audio.NewWAVWriter(request.MicrophonePath, request.SampleRate, request.Channels)
	if err != nil {
		return nil, err
	}
	system, err := audio.NewWAVWriter(request.SystemPath, request.SampleRate, request.Channels)
	if err != nil {
		_ = microphone.Close()
		return nil, err
	}
	session := &syntheticSession{microphone: microphone, system: system, silentSystem: backend.silentSystem, health: backend.health, microphoneHealth: backend.microphoneHealth, done: make(chan struct{})}
	session.wait.Add(1)
	go session.write(request.SampleRate, request.Channels)
	return session, nil
}

type syntheticSession struct {
	microphone       *audio.WAVWriter
	system           *audio.WAVWriter
	silentSystem     bool
	healthMu         sync.Mutex
	health           []nativeaudio.SystemHealth
	microphoneHealth []nativeaudio.SystemHealth
	done             chan struct{}
	once             sync.Once
	wait             sync.WaitGroup
}

// SystemHealth replays the scripted health sequence.
func (s *syntheticSession) SystemHealth() nativeaudio.SystemHealth {
	return s.nextHealth(&s.health)
}

// MicrophoneHealth replays the scripted microphone sequence.
func (s *syntheticSession) MicrophoneHealth() nativeaudio.SystemHealth {
	return s.nextHealth(&s.microphoneHealth)
}

func (s *syntheticSession) nextHealth(queue *[]nativeaudio.SystemHealth) nativeaudio.SystemHealth {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if len(*queue) == 0 {
		return nativeaudio.SystemOK
	}
	next := (*queue)[0]
	if len(*queue) > 1 {
		*queue = (*queue)[1:]
	}
	return next
}

func (s *syntheticSession) write(sampleRate, channels int) {
	defer s.wait.Done()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	frames := sampleRate / 50
	microphone := make([]byte, frames*channels*2)
	system := make([]byte, frames*channels*2)
	for i := 0; i < len(microphone); i += 2 {
		binary.LittleEndian.PutUint16(microphone[i:i+2], uint16(int16(1000)))
		binary.LittleEndian.PutUint16(system[i:i+2], uint16(int16(2000)))
	}
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			_ = s.microphone.WritePCM16(microphone)
			if !s.silentSystem {
				_ = s.system.WritePCM16(system)
			}
		}
	}
}

func TestEmptySystemStreamDoesNotFailMeeting(t *testing.T) {
	cfg := config.Defaults().Audio
	manager := newManager(cfg, log.New(io.Discard, "", 0), syntheticBackend{silentSystem: true})
	dir := t.TempDir()
	session, err := manager.Start(
		"meeting-without-system-playback",
		dir,
		16000,
		1,
		model.Device{ID: "mic", Name: "Test microphone"},
		model.Device{ID: "system", Name: "Test system"},
	)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := manager.Stop("meeting-without-system-playback"); err != nil {
		t.Fatalf("empty but valid system WAV must not fail the meeting: %v", err)
	}
	file, err := os.Open(session.SystemPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := audio.ReadWAVInfo(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if info.DataSize != 0 {
		t.Fatalf("expected an empty system stream, got %d bytes", info.DataSize)
	}
}

func (s *syntheticSession) Stop() error {
	s.once.Do(func() { close(s.done) })
	s.wait.Wait()
	if err := s.microphone.Close(); err != nil {
		return err
	}
	return s.system.Close()
}

func TestSyntheticNativeTwoStreamRecording(t *testing.T) {
	cfg := config.Defaults().Audio
	manager := newManager(cfg, log.New(io.Discard, "", 0), syntheticBackend{})
	dir := t.TempDir()
	input := model.Device{ID: "mic", Name: "Test microphone"}
	output := model.Device{ID: "system", Name: "Test system"}
	session, err := manager.Start("meeting-test", dir, 16000, 1, input, output)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if !manager.Active("meeting-test") {
		t.Fatal("recording must be active")
	}
	if _, err := manager.Stop("meeting-test"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{session.MicPath, session.SystemPath} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := audio.ReadWAVInfo(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if info.SampleRate != 16000 || info.Channels != 1 || info.DataSize < 3200 {
			t.Fatalf("unexpected WAV: %+v", info)
		}
	}
}

// TestSystemAudioRecoveryIsRecorded covers the Windows and Linux path where the
// output device disappears mid-recording: the native layer recreates the stream,
// and the recorder has to remember the event so the meeting card can show it.
func TestSystemAudioRecoveryIsRecorded(t *testing.T) {
	previous := systemWatchInterval
	systemWatchInterval = 10 * time.Millisecond
	defer func() { systemWatchInterval = previous }()

	backend := syntheticBackend{health: []nativeaudio.SystemHealth{
		nativeaudio.SystemOK,
		nativeaudio.SystemRestored,
		nativeaudio.SystemOK,
		nativeaudio.SystemLost,
	}}
	manager := newManager(config.Defaults().Audio, log.New(io.Discard, "", 0), backend)
	session, err := manager.Start(
		"meeting-device-switch",
		t.TempDir(),
		16000,
		1,
		model.Device{ID: "mic", Name: "Test microphone"},
		model.Device{ID: "system", Name: "Test system"},
	)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if event, _ := session.SystemIssue(); event == "lost" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	event, at := session.SystemIssue()
	if event != "lost" {
		t.Fatalf("system audio loss was not recorded: %q", event)
	}
	if at.IsZero() {
		t.Fatal("system audio event has no timestamp")
	}
	if _, err := manager.Stop("meeting-device-switch"); err != nil {
		t.Fatalf("recording with a restored stream must still save: %v", err)
	}
}

// TestMicrophoneRecoveryIsRecorded mirrors the system audio case: the microphone
// follows a changed input device and must leave a meeting warning.
func TestMicrophoneRecoveryIsRecorded(t *testing.T) {
	previous := systemWatchInterval
	systemWatchInterval = 10 * time.Millisecond
	defer func() { systemWatchInterval = previous }()

	backend := syntheticBackend{microphoneHealth: []nativeaudio.SystemHealth{
		nativeaudio.SystemOK,
		nativeaudio.SystemRestored,
		nativeaudio.SystemOK,
		nativeaudio.SystemLost,
	}}
	manager := newManager(config.Defaults().Audio, log.New(io.Discard, "", 0), backend)
	session, err := manager.Start(
		"meeting-microphone-switch",
		t.TempDir(),
		16000,
		1,
		model.Device{ID: "mic", Name: "Test microphone"},
		model.Device{ID: "system", Name: "Test system"},
	)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if event, _ := session.MicrophoneIssue(); event == "lost" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	event, at := session.MicrophoneIssue()
	if event != "lost" {
		t.Fatalf("microphone loss was not recorded: %q", event)
	}
	if at.IsZero() {
		t.Fatal("microphone event has no timestamp")
	}
	if event, _ := session.SystemIssue(); event != "" {
		t.Fatalf("the microphone must not change the system audio state: %q", event)
	}
	if _, err := manager.Stop("meeting-microphone-switch"); err != nil {
		t.Fatalf("recording with a recreated microphone must still save: %v", err)
	}
}
