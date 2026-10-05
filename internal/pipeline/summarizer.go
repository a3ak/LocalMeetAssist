package pipeline

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"localmeetassist/internal/config"
)

// Summarizer calls an OpenAI-compatible /chat/completions endpoint.
type Summarizer struct {
	cfg    config.Summary
	client *http.Client
}

// NewSummarizer builds a client for the configured summary endpoint.
func NewSummarizer(cfg config.Summary) (*Summarizer, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !cfg.TLSVerify, ServerName: cfg.TLSServerName} // #nosec G402: explicitly user-configurable.
	if cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificates found in summary.tls_ca_file")
		}
		tlsConfig.RootCAs = pool
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Summarizer{cfg: cfg, client: &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsConfig}}}, nil
}

// Summarize turns a transcript into minutes using the configured prompt.
func (s *Summarizer) Summarize(ctx context.Context, transcript string) (string, error) {
	if strings.TrimSpace(s.cfg.BaseURL) == "" || strings.TrimSpace(s.cfg.Model) == "" {
		return "", errors.New("summary.base_url and summary.model are required")
	}
	prompt := strings.TrimSpace(s.cfg.SystemPrompt)
	if prompt == "" && s.cfg.PromptFile != "" {
		if b, err := os.ReadFile(s.cfg.PromptFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			prompt = string(b)
		}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = config.DefaultSummaryPrompt
	}
	prompt = "Respond strictly in " + languageName(s.cfg.Language) + ".\n\n" + prompt
	messages := []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": transcript}}
	return s.complete(ctx, messages, 0)
}

// resolveSummaryConfig resolves summary.language against the interface
// language: an empty summary.language means "follow the interface language".
func resolveSummaryConfig(cfg config.Config) config.Summary {
	summary := cfg.Summary
	if strings.TrimSpace(summary.Language) == "" {
		summary.Language = cfg.App.Language
	}
	return summary
}

// languageName maps a language code to a human-readable name for the prompt
// directive. Unknown codes are passed through so a custom language still works.
func languageName(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "ru", "rus", "russian":
		return "Russian"
	case "en", "eng", "english":
		return "English"
	default:
		if strings.TrimSpace(code) == "" {
			return "Russian"
		}
		return strings.TrimSpace(code)
	}
}

// Test performs a minimal real completion so URL, TLS, authorization and the
// configured model are checked together. It intentionally uses only a few
// output tokens and does not include the meeting prompt or transcript.
func (s *Summarizer) Test(ctx context.Context) (string, error) {
	if strings.TrimSpace(s.cfg.BaseURL) == "" || strings.TrimSpace(s.cfg.Model) == "" {
		return "", errors.New("summary.base_url and summary.model are required")
	}
	return s.complete(ctx, []map[string]string{{"role": "user", "content": "Answer with one word: OK"}}, 4)
}

func (s *Summarizer) complete(ctx context.Context, messages []map[string]string, maxTokens int) (string, error) {
	body := map[string]any{"model": s.cfg.Model, "temperature": 0.1, "messages": messages}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	payload, _ := json.Marshal(body)
	url := strings.TrimRight(s.cfg.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	token := s.cfg.Token
	if token == "" && s.cfg.TokenEnv != "" {
		token = os.Getenv(s.cfg.TokenEnv)
	}
	var last error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			last = err
		} else {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			if readErr != nil {
				last = readErr
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				last = fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, tail(string(data), 800))
			} else {
				var decoded struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(data, &decoded); err != nil {
					return "", err
				}
				if len(decoded.Choices) == 0 {
					return "", errors.New("LLM response has no choices")
				}
				return strings.TrimSpace(decoded.Choices[0].Message.Content), nil
			}
		}
		if attempt < s.cfg.MaxRetries {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return "", last
}
