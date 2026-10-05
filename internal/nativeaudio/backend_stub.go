//go:build !cgo || (!windows && !darwin && !linux)

package nativeaudio

import "errors"

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
type Session interface{ Stop() error }

// Capture wraps one native capture handle.
type Capture struct{}

// New creates a native audio backend.
func New() *Backend { return &Backend{} }

// Name returns the platform backend name.
func (b *Backend) Name() string { return "native audio unavailable (CGO disabled)" }

// Devices reports that capture is unavailable without CGO.
func (b *Backend) Devices() ([]Device, []Device, error) {
	return []Device{}, []Device{}, errors.New("native audio requires CGO_ENABLED=1")
}

// Start always fails without CGO.
func (b *Backend) Start(StartRequest) (Session, error) {
	return nil, errors.New("native audio requires CGO_ENABLED=1")
}

// Stop is a no-op without CGO.
func (c *Capture) Stop() error { return nil }
