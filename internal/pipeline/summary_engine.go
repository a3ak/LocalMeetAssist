package pipeline

import (
	"context"
	"strings"

	"localmeetassist/internal/config"
)

// SummaryEngine builds the minutes of one meeting from its transcript.
//
// Two implementations exist: the network client that calls an
// OpenAI-compatible endpoint, and the local one that runs a GGUF model through
// the bundled llama.cpp runtime. Both produce plain text for the same artifact.
type SummaryEngine interface {
	Summarize(ctx context.Context, transcript string) (string, error)
	// Test performs a minimal real call so the configuration is checked without
	// using the meeting prompt or transcript.
	Test(ctx context.Context) (string, error)
}

// NewSummaryEngine builds the engine selected by summary.engine.
func NewSummaryEngine(cfg config.Config) (SummaryEngine, error) {
	summary := resolveSummaryConfig(cfg)
	if summary.LocalSummary() {
		return NewLocalSummarizer(cfg)
	}
	return NewSummarizer(summary)
}

// summarySystemPrompt returns the instruction sent to the model: the configured
// template with the answer-language directive in front of it.
//
// The answer language comes from the «Minutes language» setting, not from the
// prompt. A custom prompt describes the structure and wording; the language is
// prepended here so a template written in English still produces headings in
// the language the user picked in the settings.
func summarySystemPrompt(cfg config.Summary) string {
	prompt := strings.TrimSpace(cfg.SystemPrompt)
	if prompt == "" {
		prompt = config.EffectiveSummaryPrompt(cfg)
	}
	return "Answer language: " + languageName(cfg.Language) + ". Write the entire answer — every section title, list label and table column — in that language, regardless of the language of these instructions.\n\n" + prompt
}
