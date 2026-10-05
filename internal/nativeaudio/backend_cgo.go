//go:build cgo && (windows || darwin || linux)

package nativeaudio

/*
#cgo CFLAGS: -std=c11
#cgo linux LDFLAGS: -ldl -lpthread -lm
#cgo windows LDFLAGS: -lole32 -luuid -lksuser -lwinmm -lavrt
#cgo darwin CFLAGS: -fblocks
#cgo darwin LDFLAGS: -framework Foundation -framework CoreAudio -framework AudioToolbox -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo
#include <stdlib.h>
#include "nativeaudio.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Device is one capture device reported by the platform backend.
type Device struct {
	ID        string
	Name      string
	IsDefault bool
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
