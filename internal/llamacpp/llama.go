// Package llamacpp loads the llama.cpp native library through CGO.
//
// The library is not linked at build time: modelmanager downloads the official
// prebuilt release on first use and this package loads it with dlopen and
// dlsym, the same way internal/whispercpp loads whisper.cpp.
package llamacpp

/*
#cgo linux LDFLAGS: -ldl
#include "llama.h"
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"errors"
	"runtime"

	"unsafe"
)

// Parameters tune one local generation run. Zero values fall back to the
// defaults of the model and of llama.cpp.
type Parameters struct {
	// ContextTokens is the size of the context window. The model may advertise
	// a much larger one (Qwen3.5 has 256K); the window is kept small enough to
	// fit the machine memory together with the weights.
	ContextTokens int
	// Threads defaults to the number of CPUs, capped at 8.
	Threads int
	// GPULayers is the number of layers offloaded to the accelerator. -1 means
	// "as many as fit", which enables Metal on macOS and the GPU in the Vulkan
	// build.
	GPULayers int
	// Temperature above zero switches sampling on. Zero keeps the answer
	// deterministic, which is what minutes want.
	Temperature float32
	TopP        float32
	TopK        int
	// MaxTokens limits the length of the generated minutes.
	MaxTokens int
}

// Runtime is an open llama.cpp context bound to one GGUF model file.
type Runtime struct{ handle C.lm_llama_handle }

// DefaultParameters returns the settings used for local summarization: a
// deterministic answer, the whole model on the accelerator when possible.
func DefaultParameters() Parameters {
	threads := runtime.NumCPU()
	if threads > 8 {
		threads = 8
	}
	if threads < 1 {
		threads = 1
	}
	return Parameters{
		ContextTokens: 8192,
		Threads:       threads,
		GPULayers:     -1,
		TopK:          40,
		MaxTokens:     2048,
	}
}

// Probe checks that the native library can be loaded and exports the symbols
// the summarizer needs.
func Probe(runtimePath string) error {
	runtimeValue := C.CString(runtimePath)
	defer C.free(unsafe.Pointer(runtimeValue))
	if C.lm_llama_probe(runtimeValue) != 0 {
		return errors.New(C.GoString(C.lm_llama_last_error()))
	}
	return nil
}

// Open loads the library and creates a context for the given model.
func Open(runtimePath, modelPath string, params Parameters) (*Runtime, error) {
	if params.ContextTokens <= 0 {
		params.ContextTokens = DefaultParameters().ContextTokens
	}
	if params.Threads <= 0 {
		params.Threads = DefaultParameters().Threads
	}
	runtimeValue := C.CString(runtimePath)
	modelValue := C.CString(modelPath)
	defer C.free(unsafe.Pointer(runtimeValue))
	defer C.free(unsafe.Pointer(modelValue))
	handle := C.lm_llama_open(runtimeValue, modelValue, C.int(params.ContextTokens), C.int(params.Threads), C.int(params.GPULayers))
	if handle == nil {
		return nil, errors.New(C.GoString(C.lm_llama_last_error()))
	}
	return &Runtime{handle: handle}, nil
}

// ContextTokens reports the context size the runtime actually created.
func (r *Runtime) ContextTokens() int {
	if r == nil || r.handle == nil {
		return 0
	}
	return int(C.lm_llama_context_tokens(r.handle))
}

// CountTokens reports how many tokens the model assigns to text. It is used to
// decide whether a transcript still fits into the context before a long local
// generation is started.
func (r *Runtime) CountTokens(text string) (int, error) {
	if r == nil || r.handle == nil {
		return 0, errors.New("invalid llama.cpp request")
	}
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	count := int(C.lm_llama_count_tokens(r.handle, value))
	if count < 0 {
		return 0, errors.New(C.GoString(C.lm_llama_last_error()))
	}
	return count, nil
}

// nativeBuffer allocates a buffer that the native side fills and terminates.
func nativeBuffer(size int) []byte {
	return make([]byte, size+1)
}

// Chat formats a system/user pair with the chat template stored in the model
// and without the reasoning preamble, which is what minutes generation wants.
func (r *Runtime) Chat(systemPrompt, userMessage string) (string, error) {
	return r.ChatThinking(systemPrompt, userMessage, false)
}

// ChatThinking formats the prompt; thinking = true opens the reasoning block
// of a hybrid reasoning model so it writes its reasoning before the answer.
func (r *Runtime) ChatThinking(systemPrompt, userMessage string, thinking bool) (string, error) {
	if r == nil || r.handle == nil {
		return "", errors.New("invalid llama.cpp request")
	}
	system := C.CString(systemPrompt)
	user := C.CString(userMessage)
	defer C.free(unsafe.Pointer(system))
	defer C.free(unsafe.Pointer(user))
	// The recommended size is twice the total number of characters; a chat
	// template adds a small constant on top of that.
	capacity := 2*(len(systemPrompt)+len(userMessage)) + 1024
	buffer := nativeBuffer(capacity)
	thinkingFlag := C.int(1)
	if !thinking {
		thinkingFlag = 0
	}
	written := C.lm_llama_apply_chat(r.handle, system, user, thinkingFlag, (*C.char)(unsafe.Pointer(&buffer[0])), C.int(capacity))
	if written < 0 {
		return "", errors.New(C.GoString(C.lm_llama_last_error()))
	}
	if int(written) > capacity {
		// The runtime returned the required size instead of the formatted
		// prompt; ask again with enough room.
		buffer = nativeBuffer(int(written))
		written = C.lm_llama_apply_chat(r.handle, system, user, thinkingFlag, (*C.char)(unsafe.Pointer(&buffer[0])), C.int(len(buffer)))
		if written < 0 || int(written) > len(buffer) {
			return "", errors.New(C.GoString(C.lm_llama_last_error()))
		}
	}
	return string(buffer[:int(written)]), nil
}

// Generate completes a prompt. It aborts as soon as ctx is cancelled, so a
// cancelled meeting processing stops the generation instead of waiting for the
// last token.
func (r *Runtime) Generate(ctx context.Context, prompt string, params Parameters) (string, error) {
	if r == nil || r.handle == nil {
		return "", errors.New("invalid llama.cpp request")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultParameters().MaxTokens
	}
	promptValue := C.CString(prompt)
	defer C.free(unsafe.Pointer(promptValue))
	buffer := nativeBuffer(maxTokens*8 + 1024)
	setCancelled(false)
	stop := context.AfterFunc(ctx, func() { setCancelled(true) })
	defer stop()
	written := C.lm_llama_generate(
		r.handle,
		promptValue,
		(*C.char)(unsafe.Pointer(&buffer[0])),
		C.int(len(buffer)),
		C.int(maxTokens),
		C.float(params.Temperature),
		C.float(params.TopP),
		C.int(params.TopK),
	)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if written < 0 {
		return "", errors.New(C.GoString(C.lm_llama_last_error()))
	}
	if int(written) > len(buffer) {
		return "", errors.New("llama.cpp returned more text than the output buffer can hold")
	}
	return string(buffer[:int(written)]), nil
}

// Close releases the native context and the loaded library.
func (r *Runtime) Close() {
	if r != nil && r.handle != nil {
		C.lm_llama_close(r.handle)
		r.handle = nil
	}
}
