package native

/*
#cgo CFLAGS: -I${SRCDIR}/src -I${SRCDIR}/src/api -I${SRCDIR}/src/base -I${SRCDIR}/src/base/rtc_base -I${SRCDIR}/src/base/system_wrappers/include -I${SRCDIR}/src/audio_processing -I${SRCDIR}/src/audio_processing/include -I${SRCDIR}/src/audio_processing/aec3 -I${SRCDIR}/src/audio_processing/resampler -I${SRCDIR}/src/audio_processing/logging -I${SRCDIR}/src/audio_processing/utility -I${SRCDIR}/src/absl -I${SRCDIR}/src/base/jsoncpp/include -I${SRCDIR}/src/rtc_base/experiments -I${SRCDIR}/src/common_audio -I${SRCDIR}/src/common_audio/third_party/ooura/fft_size_128
#cgo CXXFLAGS: -std=c++20 -fexceptions -Wno-deprecated -Wno-deprecated-declarations -I${SRCDIR}/src -I${SRCDIR}/src/api -I${SRCDIR}/src/base -I${SRCDIR}/src/base/rtc_base -I${SRCDIR}/src/base/system_wrappers/include -I${SRCDIR}/src/audio_processing -I${SRCDIR}/src/audio_processing/include -I${SRCDIR}/src/audio_processing/aec3 -I${SRCDIR}/src/audio_processing/resampler -I${SRCDIR}/src/audio_processing/logging -I${SRCDIR}/src/audio_processing/utility -I${SRCDIR}/src/absl -I${SRCDIR}/src/base/jsoncpp/include -I${SRCDIR}/src/base/jsoncpp/src/lib_json -I${SRCDIR}/src/rtc_base/experiments -I${SRCDIR}/src/common_audio -I${SRCDIR}/src/common_audio/third_party/ooura/fft_size_128
#cgo linux CFLAGS: -DWEBRTC_LINUX -DWEBRTC_POSIX
#cgo linux CXXFLAGS: -DWEBRTC_LINUX -DWEBRTC_POSIX
#cgo darwin CFLAGS: -DWEBRTC_MAC -DWEBRTC_POSIX
#cgo darwin CXXFLAGS: -DWEBRTC_MAC -DWEBRTC_POSIX
#cgo windows CFLAGS: -DWEBRTC_WIN
#cgo windows CXXFLAGS: -DWEBRTC_WIN
#cgo linux LDFLAGS: -lstdc++ -lm
#cgo darwin LDFLAGS: -lc++ -lm
#cgo windows LDFLAGS: -lstdc++ -lm
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"unsafe"
)

type Canceller struct {
	handle C.lm_aec3_handle
	frame  int
}

func New(sampleRate, delayMS int) (*Canceller, error) {
	handle := C.lm_aec3_create(C.int(sampleRate), C.int(delayMS))
	if handle == nil {
		return nil, errors.New(C.GoString(C.lm_aec3_last_error()))
	}
	return &Canceller{handle: handle, frame: sampleRate / 100}, nil
}

func (c *Canceller) FrameSamples() int { return c.frame }

func (c *Canceller) Process(render, capture, output []int16) error {
	if c == nil || c.handle == nil || len(render) != c.frame || len(capture) != c.frame || len(output) != c.frame {
		return errors.New("invalid AEC3 frame")
	}
	if C.lm_aec3_process(c.handle,
		(*C.int16_t)(unsafe.Pointer(&render[0])),
		(*C.int16_t)(unsafe.Pointer(&capture[0])),
		(*C.int16_t)(unsafe.Pointer(&output[0])), C.int(c.frame)) != 0 {
		return errors.New(C.GoString(C.lm_aec3_last_error()))
	}
	return nil
}

func (c *Canceller) Close() {
	if c != nil && c.handle != nil {
		C.lm_aec3_destroy(c.handle)
		c.handle = nil
	}
}
