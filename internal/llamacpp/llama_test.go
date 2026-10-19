package llamacpp

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Integration tests for the native runtime. They are skipped unless a
// downloaded runtime and a real GGUF model are pointed to:
//
//	LM_LLAMA_RUNTIME=/path/libllama.0.dylib LM_LLAMA_MODEL=/path/model.gguf go test ./internal/llamacpp/
func integrationRuntime(t *testing.T) (*Runtime, Parameters) {
	t.Helper()
	runtimePath := os.Getenv("LM_LLAMA_RUNTIME")
	modelPath := os.Getenv("LM_LLAMA_MODEL")
	if runtimePath == "" || modelPath == "" {
		t.Skip("set LM_LLAMA_RUNTIME and LM_LLAMA_MODEL to run against a real model")
	}
	params := DefaultParameters()
	params.ContextTokens = 4096
	params.MaxTokens = 256
	runtime, err := Open(runtimePath, modelPath, params)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(runtime.Close)
	if runtime.ContextTokens() <= 0 {
		t.Fatal("runtime reported an empty context")
	}
	return runtime, params
}

// TestProbeReportsMissingSymbols checks that a file that is not the runtime is
// rejected with a readable message rather than a crash.
func TestProbeRejectsForeignLibrary(t *testing.T) {
	err := Probe("/definitely/not/here/libllama.dylib")
	if err == nil {
		t.Fatal("probing a missing library reported success")
	}
}

// TestChatClosesReasoningBlock verifies that the prompt of a hybrid reasoning
// model ends with a closed reasoning block, so the answer is the minutes and
// not the reasoning behind them.
func TestChatClosesReasoningBlock(t *testing.T) {
	runtime, _ := integrationRuntime(t)
	prompt, err := runtime.Chat("Ты помощник.", "Привет")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.HasSuffix(prompt, "<|im_start|>assistant\n<think>\n\n</think>\n\n") {
		t.Fatalf("prompt does not close the reasoning block: %q", prompt)
	}
	thinking, err := runtime.ChatThinking("Ты помощник.", "Привет", true)
	if err != nil {
		t.Fatalf("chat with thinking: %v", err)
	}
	if !strings.HasSuffix(thinking, "<|im_start|>assistant\n<think>\n") {
		t.Fatalf("thinking prompt does not open the reasoning block: %q", thinking)
	}
}

// TestGenerateAnswersWithoutReasoning checks the whole path: prompt, decode and
// sampling. Without the closed block the model returns its reasoning text.
func TestGenerateAnswersWithoutReasoning(t *testing.T) {
	runtime, params := integrationRuntime(t)
	prompt, err := runtime.Chat("Отвечай одним словом.", "Столица Франции?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	answer, err := runtime.Generate(context.Background(), prompt, params)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(answer, "Париж") {
		t.Fatalf("answer does not contain the expected word: %q", answer)
	}
	if strings.Contains(answer, "Thinking Process") {
		t.Fatalf("answer contains the reasoning preamble: %q", answer)
	}
}

// TestGenerateStopsOnCancel checks that cancelling the request context stops
// generation instead of waiting for the last token.
func TestGenerateStopsOnCancel(t *testing.T) {
	runtime, params := integrationRuntime(t)
	params.MaxTokens = 2048
	prompt, err := runtime.Chat("Ты помощник.", "Напиши длинное эссе о морских ежах.")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Generate(ctx, prompt, params); err == nil {
		t.Fatal("a cancelled request reported success")
	}
}
