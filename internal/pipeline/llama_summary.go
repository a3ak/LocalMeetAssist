package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"localmeetassist/internal/config"
	"localmeetassist/internal/llamacpp"
	"localmeetassist/internal/modelmanager"
)

// LocalSummarizer builds minutes with a GGUF model that runs on this machine
// through the bundled llama.cpp runtime. The transcript never leaves the
// computer.
type LocalSummarizer struct {
	summary     config.Summary
	runtimePath string
	// Progress reports how much of the work is done, from 0 to 1. It is called
	// between generation passes so a long meeting shows movement in the UI.
	Progress func(fraction float64)
}

// The notes prompts are the map and reduce steps used when a transcript is
// longer than the context window. They keep facts and wording instead of
// writing prose, so the final pass still receives the real material.
const notesSystemPrompt = `You prepare material for meeting minutes. You receive one part of a longer meeting transcript.

Extract, without inventing anything:
- the topics discussed and the essence of each;
- decisions that were explicitly made;
- tasks: what has to be done, who is responsible, and the deadline if it was stated;
- open questions, risks and objections;
- names, numbers, dates, product and system names exactly as they appear.

Keep the speaker labels as written, write the notes in the language of the transcript, and keep the order of the discussion. Use dense plain text bullet points, one thought per line, no headings, no markdown, no section titles, no conclusions of your own. If the part contains nothing substantive, write one line saying so.`

const mergeNotesSystemPrompt = `You combine notes taken from consecutive parts of one meeting into a single set of notes.

Merge duplicates, keep the order of the meeting, and keep every decision, task, name, number and date exactly as written. Do not add anything that is not in the notes. Use dense plain text bullet points, one thought per line, no headings and no markdown.`

// NewLocalSummarizer prepares a local minutes engine from the configuration.
func NewLocalSummarizer(cfg config.Config) (*LocalSummarizer, error) {
	summary := resolveSummaryConfig(cfg)
	if strings.TrimSpace(summary.ModelPath) == "" {
		return nil, errors.New("summary.model_path is empty: install a local model in the models window")
	}
	if summary.ContextTokens <= 0 {
		summary.ContextTokens = 8192
	}
	if summary.MaxTokens <= 0 {
		summary.MaxTokens = 2048
	}
	if summary.MaxTokens > summary.ContextTokens-1024 {
		return nil, fmt.Errorf("summary.max_tokens must leave room for the transcript in summary.context_tokens (%d)", summary.ContextTokens)
	}
	return &LocalSummarizer{summary: summary, runtimePath: modelmanager.LlamaRuntimePath(cfg)}, nil
}

// RuntimePath reports the library this summarizer loads. It is exposed for the
// model test, which verifies the runtime and the model together.
func (s *LocalSummarizer) RuntimePath() string { return s.runtimePath }

// parameters maps the configuration onto the native generation settings.
func (s *LocalSummarizer) parameters(maxTokens int) llamacpp.Parameters {
	params := llamacpp.DefaultParameters()
	params.ContextTokens = s.summary.ContextTokens
	params.Threads = s.summary.Threads
	// -1 and 0 both mean "offload as many layers as fit", which uses Metal on
	// macOS and the GPU in the Vulkan build.
	if s.summary.GPULayers <= 0 {
		params.GPULayers = -1
	} else {
		params.GPULayers = s.summary.GPULayers
	}
	params.MaxTokens = maxTokens
	params.Temperature = float32(s.summary.Temperature)
	params.TopP = 0.95
	params.TopK = 40
	return params
}

// open loads the runtime and the model.
func (s *LocalSummarizer) open() (*llamacpp.Runtime, error) {
	runtime, err := llamacpp.Open(s.runtimePath, s.summary.ModelPath, s.parameters(s.summary.MaxTokens))
	if err != nil {
		return nil, fmt.Errorf("local model %s: %w", filepath.Base(s.summary.ModelPath), err)
	}
	return runtime, nil
}

// report forwards progress to the caller, if it asked for it.
func (s *LocalSummarizer) report(fraction float64) {
	if s.Progress == nil {
		return
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	s.Progress(fraction)
}

// Summarize builds the minutes of one meeting.
func (s *LocalSummarizer) Summarize(ctx context.Context, transcript string) (string, error) {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return "", errors.New("the transcript is empty")
	}
	runtime, err := s.open()
	if err != nil {
		return "", err
	}
	defer runtime.Close()
	system := summarySystemPrompt(s.summary)
	budget, err := s.messageBudget(runtime, system)
	if err != nil {
		return "", err
	}
	tokens, err := runtime.CountTokens(transcript)
	if err != nil {
		return "", err
	}
	if tokens <= budget {
		s.report(0.3)
		answer, err := s.generate(ctx, runtime, system, transcript, s.summary.MaxTokens)
		s.report(1)
		return answer, err
	}
	// The transcript does not fit: take notes part by part and write the
	// minutes from the notes. A meeting is rarely short enough for the context
	// window on its own.
	notes, err := s.notes(ctx, runtime, transcript, budget)
	if err != nil {
		return "", err
	}
	const notesHeader = "Notes from the whole meeting, in order:\n\n"
	finalBudget, err := s.messageBudget(runtime, system)
	if err != nil {
		return "", err
	}
	headerTokens, err := runtime.CountTokens(notesHeader)
	if err != nil {
		return "", err
	}
	notes, err = s.fit(ctx, runtime, notes, finalBudget-headerTokens, mergeNotesSystemPrompt)
	if err != nil {
		return "", err
	}
	s.report(0.9)
	answer, err := s.generate(ctx, runtime, system, notesHeader+notes, s.summary.MaxTokens)
	s.report(1)
	if err != nil {
		return "", err
	}
	return answer, nil
}

// messageBudget returns how many tokens a user message may use so that the
// system prompt and the answer still fit into the context.
func (s *LocalSummarizer) messageBudget(runtime *llamacpp.Runtime, system string) (int, error) {
	systemTokens, err := runtime.CountTokens(system)
	if err != nil {
		return 0, err
	}
	budget := s.summary.ContextTokens - systemTokens - s.summary.MaxTokens - 64
	if budget < 256 {
		return 0, fmt.Errorf("summary.context_tokens (%d) leaves no room for the transcript: the prompt and the answer already take %d", s.summary.ContextTokens, systemTokens+s.summary.MaxTokens)
	}
	return budget, nil
}

// generate runs one pass and rejects an empty answer.
func (s *LocalSummarizer) generate(ctx context.Context, runtime *llamacpp.Runtime, system, message string, maxTokens int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prompt, err := runtime.Chat(system, message)
	if err != nil {
		return "", err
	}
	answer, err := runtime.Generate(ctx, prompt, s.parameters(maxTokens))
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", errors.New("the local model returned an empty answer")
	}
	return answer, nil
}

// notes takes notes of every part of a transcript that does not fit into the
// context window.
func (s *LocalSummarizer) notes(ctx context.Context, runtime *llamacpp.Runtime, transcript string, budget int) (string, error) {
	chunkBudget, err := s.messageBudget(runtime, notesSystemPrompt)
	if err != nil {
		return "", err
	}
	if chunkBudget > budget {
		chunkBudget = budget
	}
	chunks := splitForTokens(transcript, chunkBudget, runtime.CountTokens)
	if len(chunks) == 0 {
		return "", errors.New("the transcript could not be split into parts")
	}
	parts := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		notes, err := s.generate(ctx, runtime, notesSystemPrompt, chunk, 1024)
		if err != nil {
			return "", fmt.Errorf("notes of part %d/%d: %w", index+1, len(chunks), err)
		}
		parts = append(parts, notes)
		// The map step takes the first half of the progress, the reduce and the
		// final pass the rest.
		s.report(0.5 * float64(index+1) / float64(len(chunks)))
	}
	return strings.Join(parts, "\n\n"), nil
}

// fit reduces text until it fits into the token budget by merging parts.
func (s *LocalSummarizer) fit(ctx context.Context, runtime *llamacpp.Runtime, text string, budget int, system string) (string, error) {
	// Every pass shrinks the text by roughly context/1024 times, so a few
	// rounds are enough even for a very long meeting.
	for round := 0; round < 4; round++ {
		tokens, err := runtime.CountTokens(text)
		if err != nil {
			return "", err
		}
		if tokens <= budget {
			return text, nil
		}
		parts := splitForTokens(text, budget, runtime.CountTokens)
		merged := make([]string, 0, len(parts))
		for _, part := range parts {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			result, err := s.generate(ctx, runtime, system, part, 1024)
			if err != nil {
				return "", fmt.Errorf("merging notes: %w", err)
			}
			merged = append(merged, result)
		}
		text = strings.Join(merged, "\n\n")
	}
	return "", errors.New("the notes could not be reduced to fit the context window")
}

// splitForTokens splits text into consecutive parts that each fit into the
// token budget. Lines are kept whole, so a speaker turn is never cut in half.
func splitForTokens(text string, budget int, count func(string) (int, error)) []string {
	if budget <= 0 {
		return nil
	}
	var parts []string
	var current []string
	currentTokens := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		parts = append(parts, strings.Join(current, "\n"))
		current, currentTokens = nil, 0
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		tokens, err := count(line)
		if err != nil || tokens <= 0 {
			tokens = len([]rune(line))/3 + 1
		}
		if tokens > budget {
			// A single line longer than the whole budget is cut by words so it
			// still fits; this only happens when one speaker holds the floor
			// for a very long time.
			flush()
			parts = append(parts, splitLongLine(line, budget, count)...)
			continue
		}
		if currentTokens+tokens > budget && len(current) > 0 {
			flush()
		}
		current = append(current, line)
		currentTokens += tokens
	}
	flush()
	return parts
}

// splitLongLine cuts one line into words-sized pieces that fit the budget.
func splitLongLine(line string, budget int, count func(string) (int, error)) []string {
	var parts []string
	var current []string
	currentTokens := 0
	for _, word := range strings.Fields(line) {
		tokens, err := count(word)
		if err != nil || tokens <= 0 {
			tokens = len([]rune(word))/3 + 1
		}
		if currentTokens+tokens > budget && len(current) > 0 {
			parts = append(parts, strings.Join(current, " "))
			current, currentTokens = nil, 0
		}
		current = append(current, word)
		currentTokens += tokens
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, " "))
	}
	return parts
}

// Test loads the runtime and the model and generates a few tokens, so the
// settings check covers the library, the model file and generation together.
func (s *LocalSummarizer) Test(ctx context.Context) (string, error) {
	runtime, err := s.open()
	if err != nil {
		return "", err
	}
	defer runtime.Close()
	prompt, err := runtime.Chat("Answer with a single word.", "Are you ready? Answer with one word.")
	if err != nil {
		return "", err
	}
	return runtime.Generate(ctx, prompt, s.parameters(8))
}
