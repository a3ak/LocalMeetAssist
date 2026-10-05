// Package whispercpp loads the whisper.cpp native library through CGO.
package whispercpp

/*
#cgo linux LDFLAGS: -ldl
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// Runtime is an open whisper.cpp context bound to one model file.
type Runtime struct{ handle C.lm_whisper_handle }

// Segment is one recognized text span with its time range.
type Segment struct {
	StartMS int64
	EndMS   int64
	Text    string
}

// Probe checks that the native library can be loaded.
func Probe(runtimePath string) error {
	runtimeValue := C.CString(runtimePath)
	defer C.free(unsafe.Pointer(runtimeValue))
	if C.lm_whisper_probe(runtimeValue) != 0 {
		return errors.New(C.GoString(C.lm_whisper_last_error()))
	}
	return nil
}

// Open loads the library and creates a context for the given model.
func Open(runtimePath, modelPath, language string, threads int) (*Runtime, error) {
	if threads <= 0 {
		threads = runtime.NumCPU()
		if threads > 8 {
			threads = 8
		}
	}
	runtimeValue := C.CString(runtimePath)
	modelValue := C.CString(modelPath)
	languageValue := C.CString(language)
	defer C.free(unsafe.Pointer(runtimeValue))
	defer C.free(unsafe.Pointer(modelValue))
	defer C.free(unsafe.Pointer(languageValue))
	handle := C.lm_whisper_open(runtimeValue, modelValue, languageValue, C.int(threads))
	if handle == nil {
		return nil, errors.New(C.GoString(C.lm_whisper_last_error()))
	}
	return &Runtime{handle: handle}, nil
}

// Transcribe runs recognition over 16 kHz mono float samples.
func (r *Runtime) Transcribe(samples []float32) ([]Segment, error) {
	if r == nil || r.handle == nil || len(samples) == 0 {
		return nil, errors.New("invalid whisper.cpp request")
	}
	if C.lm_whisper_run(r.handle, (*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples))) != 0 {
		return nil, errors.New(C.GoString(C.lm_whisper_last_error()))
	}
	count := int(C.lm_whisper_segment_count(r.handle))
	result := make([]Segment, 0, count)
	for index := 0; index < count; index++ {
		result = append(result, Segment{
			StartMS: int64(C.lm_whisper_segment_start(r.handle, C.int(index))),
			EndMS:   int64(C.lm_whisper_segment_end(r.handle, C.int(index))),
			Text:    C.GoString(C.lm_whisper_segment_text(r.handle, C.int(index))),
		})
	}
	return result, nil
}

// Close releases the native context.
func (r *Runtime) Close() {
	if r != nil && r.handle != nil {
		C.lm_whisper_close(r.handle)
		r.handle = nil
	}
}
