//go:build cgo && (windows || darwin || linux)

package nativeaudio

/*
#cgo CFLAGS: -std=c11
#cgo linux LDFLAGS: -ldl -lpthread -lm
#cgo windows LDFLAGS: -lole32 -luuid -lksuser -lwinmm -lavrt
#cgo darwin CFLAGS: -fblocks
#cgo darwin LDFLAGS: -framework AppKit -framework Foundation -framework CoreAudio -framework AudioToolbox -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo
#include <stdlib.h>
#include "nativeaudio.h"
// Declared here as well, so the cgo binding does not depend on the header
// carrying the declaration: a partially applied change used to break the build
// with "undefined: C.lm_audio_active_input_app".
int lm_audio_active_input_app(char* name, size_t name_size, int* found, char* error_text, size_t error_size);
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// Device is one capture device reported by the platform backend.
type Device struct {
	ID        string
	Name      string
	IsDefault bool
}

// Activity is a snapshot of operating-system audio session activity. Active
// means that a client holds a running capture/playback session; it is not an
// amplitude measurement.
type Activity struct {
	Microphone bool   `json:"microphone"`
	System     bool   `json:"system"`
	Available  bool   `json:"available"`
	Backend    string `json:"backend"`
	Warning    string `json:"warning,omitempty"`
	// MicrophoneApp is the localized name of the process that is actively
	// capturing input, when the platform can attribute it. Empty when unknown.
	MicrophoneApp string `json:"microphone_app,omitempty"`
}

// StartRequest describes the two capture streams to open.
type StartRequest struct {
	MicrophoneID   string
	SystemID       string
	MicrophonePath string
	SystemPath     string
	SampleRate     int
	Channels       int
}

// Backend is the CGO capture implementation.
type Backend struct{}

// Session is a running capture that can be stopped.
type Session interface {
	Stop() error
}

// Capture wraps one native capture handle.
type Capture struct {
	mu      sync.Mutex
	handle  *C.lm_capture
	stopped bool
}

// New creates a native audio backend.
func New() *Backend { return &Backend{} }

// Name returns the platform backend name.
func (b *Backend) Name() string {
	return C.GoString(C.lm_audio_backend_name())
}

// Devices enumerates microphones and system audio sources.
func (b *Backend) Devices() (microphones, system []Device, err error) {
	microphones, err = listDevices(C.LM_DEVICE_MICROPHONE)
	if err != nil {
		return nil, nil, err
	}
	system, err = listDevices(C.LM_DEVICE_SYSTEM)
	if err != nil {
		return nil, nil, err
	}
	return microphones, system, nil
}

// Activity reports whether other applications are actively using microphone
// capture and system playback. Linux uses the PulseAudio/PipeWire compatibility
// API with an ALSA fallback; Windows and macOS query native session state.
func (b *Backend) Activity() Activity {
	if runtime.GOOS == "linux" {
		return linuxAudioActivity(b.Name())
	}
	errbuf := make([]byte, 2048)
	var microphone, system C.int
	result := C.lm_audio_activity(&microphone, &system, (*C.char)(unsafe.Pointer(&errbuf[0])), C.size_t(len(errbuf)))
	activity := Activity{Microphone: microphone != 0, System: system != 0, Available: result == 0, Backend: b.Name()}
	if result != 0 {
		activity.Warning = cString(errbuf)
	}
	if activity.Microphone {
		activity.MicrophoneApp = b.ActiveInputApp()
	}
	return activity
}

// ActiveInputApp returns the localized name of the process that is actively
// capturing the microphone. Linux resolves it from pactl, macOS from CoreAudio
// process objects; Windows reports an empty name for now.
func (b *Backend) ActiveInputApp() string {
	namebuf := make([]byte, 512)
	errbuf := make([]byte, 256)
	found := C.int(0)
	result := C.lm_audio_active_input_app(
		(*C.char)(unsafe.Pointer(&namebuf[0])), C.size_t(len(namebuf)), &found,
		(*C.char)(unsafe.Pointer(&errbuf[0])), C.size_t(len(errbuf)),
	)
	if result != 0 || found == 0 {
		return ""
	}
	return cString(namebuf)
}

func listDevices(kind C.int) ([]Device, error) {
	var devices *C.lm_device_info
	var count C.size_t
	errbuf := make([]byte, 2048)
	result := C.lm_audio_list_devices(
		kind,
		&devices,
		&count,
		(*C.char)(unsafe.Pointer(&errbuf[0])),
		C.size_t(len(errbuf)),
	)
	if result != 0 {
		return nil, errors.New(cString(errbuf))
	}
	defer C.lm_audio_free_devices(devices)
	if count == 0 {
		return []Device{}, nil
	}
	items := unsafe.Slice(devices, int(count))
	output := make([]Device, 0, int(count))
	for i := range items {
		output = append(output, Device{
			ID:        C.GoString(&items[i].id[0]),
			Name:      C.GoString(&items[i].name[0]),
			IsDefault: items[i].is_default != 0,
		})
	}
	return output, nil
}

// Start opens both capture streams and begins writing WAV files.
func (b *Backend) Start(request StartRequest) (Session, error) {
	if request.SampleRate <= 0 || request.Channels <= 0 {
		return nil, errors.New("invalid native audio format")
	}
	microphoneID := C.CString(request.MicrophoneID)
	systemID := C.CString(request.SystemID)
	microphonePath := C.CString(request.MicrophonePath)
	systemPath := C.CString(request.SystemPath)
	defer C.free(unsafe.Pointer(microphoneID))
	defer C.free(unsafe.Pointer(systemID))
	defer C.free(unsafe.Pointer(microphonePath))
	defer C.free(unsafe.Pointer(systemPath))
	errbuf := make([]byte, 4096)
	handle := C.lm_audio_start(
		microphoneID,
		systemID,
		microphonePath,
		systemPath,
		C.uint(request.SampleRate),
		C.uint(request.Channels),
		(*C.char)(unsafe.Pointer(&errbuf[0])),
		C.size_t(len(errbuf)),
	)
	if handle == nil {
		message := cString(errbuf)
		if message == "" {
			message = "native audio backend failed to start"
		}
		return nil, errors.New(message)
	}
	return &Capture{handle: handle}, nil
}

// Stop finalizes the capture and releases the native handle.
func (capture *Capture) Stop() error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.stopped {
		return nil
	}
	capture.stopped = true
	errbuf := make([]byte, 4096)
	result := C.lm_audio_stop(
		capture.handle,
		(*C.char)(unsafe.Pointer(&errbuf[0])),
		C.size_t(len(errbuf)),
	)
	capture.handle = nil
	if result != 0 {
		message := cString(errbuf)
		if message == "" {
			message = "native audio backend failed to stop"
		}
		return fmt.Errorf("native audio: %s", message)
	}
	return nil
}

func cString(buffer []byte) string {
	for i, value := range buffer {
		if value == 0 {
			return string(buffer[:i])
		}
	}
	return string(buffer)
}
