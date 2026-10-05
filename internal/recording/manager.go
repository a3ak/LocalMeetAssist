// Package recording manages native microphone and system audio capture.
package recording

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
	"localmeetassist/internal/nativeaudio"
)

// DeviceReport lists the available capture devices and warnings.
type DeviceReport struct {
	Backend       string         `json:"backend"`
	Available     bool           `json:"available"`
	Microphones   []model.Device `json:"microphones"`
	SystemSources []model.Device `json:"system_sources"`
	Warnings      []string       `json:"warnings"`
}

// Backend abstracts the platform capture implementation.
type Backend interface {
	Name() string
	Devices() ([]nativeaudio.Device, []nativeaudio.Device, error)
	Start(nativeaudio.StartRequest) (nativeaudio.Session, error)
}

// Session is one active recording with its target files.
type Session struct {
	MeetingUID   string
	Dir          string
	MicPath      string
	SystemPath   string
	StartedAt    time.Time
	InputDevice  model.Device
	OutputDevice model.Device
	capture      nativeaudio.Session
}

// Manager owns the active recording sessions.
type Manager struct {
	mu         sync.Mutex
	active     map[string]*Session
	cfg        config.Audio
	logger     *log.Logger
	backend    Backend
	deviceGate chan struct{}
	deviceMu   sync.Mutex
	deviceLast DeviceReport
	deviceAt   time.Time
}

// NewManager creates a manager with the native audio backend.
func NewManager(cfg config.Audio, logger *log.Logger) *Manager {
	return newManager(cfg, logger, nativeaudio.New())
}

// NewManagerWithBackend creates a manager with an injected backend (tests).
func NewManagerWithBackend(cfg config.Audio, logger *log.Logger, backend Backend) *Manager {
	return newManager(cfg, logger, backend)
}

func newManager(cfg config.Audio, logger *log.Logger, backend Backend) *Manager {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Manager{active: make(map[string]*Session), cfg: cfg, logger: logger, backend: backend, deviceGate: gate}
}

// UpdateConfig replaces the audio settings while nothing is recording.
func (m *Manager) UpdateConfig(cfg config.Audio) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.active) != 0 {
		return errors.New("audio settings cannot be changed while recording")
	}
	m.cfg = cfg
	return nil
}

// AnyActive reports whether any recording is in progress.
func (m *Manager) AnyActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active) != 0
}

// Devices enumerates input and output devices with a short cache.
func (m *Manager) Devices(ctx context.Context) DeviceReport {
	// Native device enumeration can block inside an OS framework. Run at most
	// one scan and make the caller's context an actual deadline instead of a
	// cosmetic pre-check. A short cache also prevents UI refreshes from hitting
	// CoreAudio/WASAPI/PipeWire repeatedly.
	m.deviceMu.Lock()
	if !m.deviceAt.IsZero() && time.Since(m.deviceAt) < 10*time.Second {
		cached := cloneDeviceReport(m.deviceLast)
		m.deviceMu.Unlock()
		m.addConfiguredDevices(&cached)
		return cached
	}
	m.deviceMu.Unlock()

	select {
	case <-ctx.Done():
		return m.deviceTimeoutReport(ctx.Err())
	case <-m.deviceGate:
	}
	type result struct {
		microphones []nativeaudio.Device
		system      []nativeaudio.Device
		err         error
	}
	resultCh := make(chan result, 1)
	go func() {
		defer func() { m.deviceGate <- struct{}{} }()
		microphones, system, err := m.backend.Devices()
		resultCh <- result{microphones: microphones, system: system, err: err}
	}()
	select {
	case <-ctx.Done():
		return m.deviceTimeoutReport(ctx.Err())
	case scanned := <-resultCh:
		report := m.buildDeviceReport(scanned.microphones, scanned.system, scanned.err)
		m.deviceMu.Lock()
		m.deviceLast = cloneDeviceReport(report)
		m.deviceAt = time.Now()
		m.deviceMu.Unlock()
		m.addConfiguredDevices(&report)
		return report
	}
}

func (m *Manager) buildDeviceReport(microphones, system []nativeaudio.Device, err error) DeviceReport {
	report := DeviceReport{
		Backend:       m.backend.Name(),
		Microphones:   []model.Device{},
		SystemSources: []model.Device{},
		Warnings:      []string{},
	}
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
	} else {
		report.Microphones = convertDevices(microphones)
		report.SystemSources = convertDevices(system)
	}
	report.Available = err == nil
	if len(report.Microphones) == 0 {
		report.Warnings = append(report.Warnings, "no microphones found")
	}
	if len(report.SystemSources) == 0 {
		report.Warnings = append(report.Warnings, systemSourceHint())
	}
	return report
}

func (m *Manager) deviceTimeoutReport(err error) DeviceReport {
	m.deviceMu.Lock()
	report := cloneDeviceReport(m.deviceLast)
	m.deviceMu.Unlock()
	report.Backend = m.backend.Name()
	report.Available = false
	report.Warnings = append(report.Warnings, "audio device scan timed out: "+err.Error())
	m.addConfiguredDevices(&report)
	return report
}

func cloneDeviceReport(input DeviceReport) DeviceReport {
	input.Microphones = append([]model.Device(nil), input.Microphones...)
	input.SystemSources = append([]model.Device(nil), input.SystemSources...)
	input.Warnings = append([]string(nil), input.Warnings...)
	return input
}

func convertDevices(input []nativeaudio.Device) []model.Device {
	sorted := append([]nativeaudio.Device(nil), input...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].IsDefault != sorted[j].IsDefault {
			return sorted[i].IsDefault
		}
		return sorted[i].Name < sorted[j].Name
	})
	output := make([]model.Device, 0, len(sorted))
	for _, device := range sorted {
		output = append(output, model.Device{ID: device.ID, Name: device.Name})
	}
	return output
}

func systemSourceHint() string {
	return "no system audio source found: Windows uses WASAPI loopback, macOS uses ScreenCaptureKit, Linux uses a PulseAudio/PipeWire monitor source"
}

func (m *Manager) addConfiguredDevices(report *DeviceReport) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	promote := func(items *[]model.Device, id, name, kind string) {
		if id == "" && name == "" {
			return
		}
		for index, device := range *items {
			if id != "" && device.ID == id || name != "" && device.Name == name {
				if index > 0 {
					selected := device
					copy((*items)[1:index+1], (*items)[0:index])
					(*items)[0] = selected
				}
				return
			}
		}
		if id == "" || !validNativeDeviceID(id) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s named %q from config.toml was not found", kind, name))
			return
		}
		if name == "" {
			name = id
		}
		*items = append([]model.Device{{ID: id, Name: name}}, *items...)
	}
	promote(&report.Microphones, cfg.InputDeviceID, cfg.InputDeviceName, "microphone")
	promote(&report.SystemSources, cfg.OutputDeviceID, cfg.OutputDeviceName, "system audio source")
}

func validNativeDeviceID(id string) bool {
	if id == "screencapturekit:system" {
		return true
	}
	if len(id) != 512 {
		return false
	}
	for _, char := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return true
}

// Start opens both capture streams and begins writing WAV files.
func (m *Manager) Start(uid, dir string, sampleRate, channels int, input, output model.Device) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.active) > 0 {
		return nil, errors.New("another recording is already in progress")
	}
	if input.ID == "" || output.ID == "" {
		return nil, errors.New("no microphone and system audio source selected")
	}
	audioDir := filepath.Join(dir, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		return nil, err
	}
	session := &Session{
		MeetingUID:   uid,
		Dir:          dir,
		MicPath:      filepath.Join(audioDir, "microphone.wav"),
		SystemPath:   filepath.Join(audioDir, "system.wav"),
		StartedAt:    time.Now(),
		InputDevice:  input,
		OutputDevice: output,
	}
	_ = os.Remove(session.MicPath)
	_ = os.Remove(session.SystemPath)
	m.logger.Printf("native recording start uid=%s backend=%q microphone=%q system=%q", uid, m.backend.Name(), input.Name, output.Name)
	capture, err := m.backend.Start(nativeaudio.StartRequest{
		MicrophoneID:   input.ID,
		SystemID:       output.ID,
		MicrophonePath: session.MicPath,
		SystemPath:     session.SystemPath,
		SampleRate:     sampleRate,
		Channels:       channels,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.backend.Name(), err)
	}
	session.capture = capture
	m.active[uid] = session
	return session, nil
}

// Stop finalizes the WAV files and validates their headers.
func (m *Manager) Stop(uid string) (*Session, error) {
	m.mu.Lock()
	session := m.active[uid]
	if session != nil {
		delete(m.active, uid)
	}
	m.mu.Unlock()
	if session == nil {
		return nil, errors.New("recording not found")
	}
	if err := session.capture.Stop(); err != nil {
		return session, err
	}
	micInfo, err := validateWAV(session.MicPath, "microphone", true)
	if err != nil {
		return session, err
	}
	systemInfo, err := validateWAV(session.SystemPath, "system", false)
	if err != nil {
		return session, err
	}
	if systemInfo.DataSize == 0 {
		appLogging.Warnf(m.logger, "native recording warning uid=%s source=system reason=%q", uid, "empty stream: no system audio played during the meeting, or macOS did not deliver audio samples")
	}
	m.logger.Printf("native recording stopped uid=%s duration=%s microphone_bytes=%d system_bytes=%d", uid, time.Since(session.StartedAt).Round(time.Millisecond), micInfo.DataSize, systemInfo.DataSize)
	return session, nil
}

func validateWAV(path, source string, requireData bool) (audio.WAVInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return audio.WAVInfo{}, fmt.Errorf("%s WAV: %w", source, err)
	}
	defer file.Close()
	info, err := audio.ReadWAVInfo(file)
	if err != nil {
		return audio.WAVInfo{}, fmt.Errorf("corrupt WAV %s: %w", source, err)
	}
	if requireData && info.DataSize == 0 {
		return info, fmt.Errorf("WAV %s contains no audio data", source)
	}
	return info, nil
}

// Active reports whether the given meeting is recording.
func (m *Manager) Active(uid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[uid] != nil
}

// StopActive stops whichever recording is currently active.
func (m *Manager) StopActive() (*Session, error) {
	m.mu.Lock()
	var uid string
	for activeUID := range m.active {
		uid = activeUID
		break
	}
	m.mu.Unlock()
	if uid == "" {
		return nil, nil
	}
	return m.Stop(uid)
}
