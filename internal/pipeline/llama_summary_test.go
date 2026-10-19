package pipeline

import (
	"strings"
	"testing"

	"localmeetassist/internal/config"
)

// wordCount is a cheap stand-in for the tokenizer: one token per word.
func wordCount(text string) (int, error) {
	return len(strings.Fields(text)), nil
}

// TestSplitForTokensKeepsEveryLine guards the map step: every transcript line
// has to land in exactly one part, and no part may exceed the budget except a
// single line that is longer than the whole budget.
func TestSplitForTokensKeepsEveryLine(t *testing.T) {
	lines := []string{
		"[00:01] Анна: первый вопрос про релиз",
		"[00:07] Борис: второй вопрос, чуть длиннее первого",
		"[00:12] Анна: третий",
		"[00:20] Борис: четвёртый вопрос с деталями и уточнениями",
		"[00:31] Анна: пятый",
	}
	transcript := strings.Join(lines, "\n")
	parts := splitForTokens(transcript, 8, wordCount)
	if len(parts) < 2 {
		t.Fatalf("a transcript longer than the budget was not split: %v", parts)
	}
	rejoined := strings.Join(parts, "\n")
	if rejoined != transcript {
		t.Fatalf("splitting lost or reordered lines:\n got %q\nwant %q", rejoined, transcript)
	}
	for _, part := range parts {
		if words := len(strings.Fields(part)); words > 8 {
			t.Fatalf("part exceeds the budget: %d words in %q", words, part)
		}
	}
}

// TestSplitForTokensCutsLongLine covers a single speaker turn that is longer
// than the whole budget: it is cut by words instead of being dropped.
func TestSplitForTokensCutsLongLine(t *testing.T) {
	line := "[00:00] Анна: " + strings.Repeat("слово ", 40)
	parts := splitForTokens(line, 10, wordCount)
	if len(parts) < 2 {
		t.Fatalf("a single long line was not cut: %v", parts)
	}
	for _, part := range parts {
		if words := len(strings.Fields(part)); words > 10 {
			t.Fatalf("cut piece exceeds the budget: %d words in %q", words, part)
		}
	}
	if joined := strings.Join(parts, " "); strings.Join(strings.Fields(joined), " ") != strings.Join(strings.Fields(line), " ") {
		t.Fatal("cutting a long line lost words")
	}
}

// TestSplitForTokensEmptyBudget guards against a division-like blow-up when the
// context window leaves no room for the transcript.
func TestSplitForTokensEmptyBudget(t *testing.T) {
	if parts := splitForTokens("какой-то текст", 0, wordCount); parts != nil {
		t.Fatalf("a zero budget produced parts: %v", parts)
	}
}

// TestNewLocalSummarizerRejectsImpossibleSettings covers the configuration
// mistakes that would otherwise fail only when a meeting is processed.
func TestNewLocalSummarizerRejectsImpossibleSettings(t *testing.T) {
	cfg := config.Defaults()
	cfg.Summary.Engine = config.EngineLocalLlama
	cfg.Summary.ModelPath = ""
	if _, err := NewLocalSummarizer(cfg); err == nil {
		t.Fatal("an empty model path was accepted")
	}
	cfg.Summary.ModelPath = "/models/llm/model.gguf"
	cfg.Summary.ContextTokens = 8192
	cfg.Summary.MaxTokens = 8192
	if _, err := NewLocalSummarizer(cfg); err == nil {
		t.Fatal("an answer longer than the context was accepted")
	}
	cfg.Summary.MaxTokens = 2048
	local, err := NewLocalSummarizer(cfg)
	if err != nil {
		t.Fatalf("valid settings were rejected: %v", err)
	}
	if local.summary.ContextTokens != 8192 {
		t.Fatalf("context was not taken from the configuration: %d", local.summary.ContextTokens)
	}
	if local.parameters(64).GPULayers != -1 {
		t.Fatal("the zero value of gpu_layers must mean 'all layers that fit'")
	}
}
