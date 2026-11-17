package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"localmeetassist/internal/config"
)

func TestSwitchingToNetworkEndpointReleasesLocalModel(t *testing.T) {
	s := newModelsServer(t)
	var qwenPath string
	for _, status := range s.models.Statuses() {
		if status.ID == "qwen3-5-4b-q4" {
			qwenPath = status.Path
			break
		}
	}
	if qwenPath == "" {
		t.Fatal("qwen3-5-4b-q4 is not listed")
	}
	cfg := s.config()
	cfg.Summary.Engine = config.EngineLocalLlama
	cfg.Summary.ModelPath = qwenPath
	s.setConfig(cfg)
	if err := s.models.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	selected := func() bool {
		for _, status := range s.models.Statuses() {
			if status.ID == "qwen3-5-4b-q4" {
				return status.Selected
			}
		}
		return false
	}
	if !selected() {
		t.Fatal("the local Qwen model must be selected while the local engine is active")
	}
	body := bytes.NewBufferString(`{"values":{"summary.engine":"openai"}}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", body)
	recorder := httptest.NewRecorder()
	s.settings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("saving the endpoint engine failed: %d %s", recorder.Code, recorder.Body.String())
	}
	if s.config().Summary.Engine != config.EngineOpenAI {
		t.Fatalf("engine=%q, want %q", s.config().Summary.Engine, config.EngineOpenAI)
	}
	if selected() {
		t.Fatal("after switching to the network endpoint the local Qwen model must be releasable")
	}
}
