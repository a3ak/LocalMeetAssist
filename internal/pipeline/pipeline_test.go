package pipeline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func TestPipelineGigaAMUsesCombinedSystemResult(t *testing.T) {
	t.Skip("obsolete external GigaAMGUI API integration")
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("audio file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if header.Filename == "system.wav" {
			if got := r.FormValue("response_format"); got != "diarized_json" {
				t.Errorf("system response_format=%q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"duration": 1.0, "segments": []map[string]any{
				{"start": 0.0, "end": 0.45, "speaker": "A", "text": "Первый участник."},
				{"start": 0.5, "end": 1.0, "speaker": "B", "text": "Второй участник."},
			}})
			return
		}
		if got := r.FormValue("response_format"); got != "verbose_json" {
			t.Errorf("microphone response_format=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"duration": 1.0, "segments": []map[string]any{
			{"start": 0.0, "end": 0.4, "text": "Владелец говорит."},
		}})
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Storage.AudioAfterProcessing = "wav"
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "gigaam-api"
	cfg.Transcription.GigaAMBaseURL = server.URL
	cfg.Transcription.SourceMode = "separate"
	cfg.Diarization.AutoRun = true
	cfg.Diarization.Engine = "gigaam-api"
	cfg.Diarization.GigaAMBackend = "onnx"
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	m := model.Meeting{UID: "01999999-9999-7999-8999-999999999991", Title: "GigaAM", StartedAt: started, Status: "processing", CreatedAt: started, UpdatedAt: started, Metadata: map[string]string{"participant_count": "3"}}
	if err := st.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	createTestWAVs(t, filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio"))
	if err := New(cfg, st).Run(context.Background(), m.UID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("expected microphone + combined system requests, got %d", requestCount)
	}
	for _, phrase := range []string{"Владелец говорит", "Первый участник", "Второй участник"} {
		if !strings.Contains(got.Transcript, phrase) {
			t.Fatalf("transcript misses %q: %s", phrase, got.Transcript)
		}
	}
	if got.SpeakerCount != 3 {
		t.Fatalf("expected owner and two remote speakers, got %d", got.SpeakerCount)
	}
}

func TestPipelineMockEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "mock"
	cfg.Diarization.AutoRun = true
	cfg.Diarization.Engine = "mock"
	cfg.Summary.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	m := model.Meeting{UID: "01999999-9999-7999-8999-999999999999", Title: "Demo", StartedAt: started, Status: "processing", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	meetingDir := filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio")
	for _, name := range []string{"microphone.wav", "system.wav"} {
		w, err := audio.NewWAVWriter(filepath.Join(meetingDir, name), 16000, 1)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]byte, 3200)
		for i := 0; i < len(pcm); i += 2 {
			binary.LittleEndian.PutUint16(pcm[i:i+2], uint16(int16(100)))
		}
		if err := w.WritePCM16(pcm); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := New(cfg, st).Run(context.Background(), m.UID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.Transcript == "" || got.SpeakerCount != 2 {
		t.Fatalf("unexpected meeting: %+v", got)
	}
	arts, err := st.Artifacts(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) < 4 {
		t.Fatalf("expected artifacts, got %d", len(arts))
	}
	for _, a := range arts {
		if a.Type == "mixed_wav" {
			if _, err := os.Stat(a.Path); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("mixed_wav artifact missing")
}

func TestRemoteSpeakerCountUsesTotalParticipants(t *testing.T) {
	if got := remoteSpeakerCount(map[string]string{"participant_count": "8"}); got != 7 {
		t.Fatalf("expected 7 system speakers, got %d", got)
	}
	if got := remoteSpeakerCount(map[string]string{"participant_count": "1"}); got != 0 {
		t.Fatalf("one microphone participant must keep auto system count, got %d", got)
	}
}

func TestCancelStopsActiveSummaryAndMarksMeeting(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-releaseServer:
		}
	}))
	defer server.Close()
	defer close(releaseServer)

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Summary.AutoRun = true
	cfg.Summary.BaseURL = server.URL
	cfg.Summary.Model = "test-model"
	cfg.Summary.MaxRetries = 0
	cfg.Summary.TimeoutSeconds = 30
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "01999999-9999-7999-8999-999999999990", Title: "Cancel", StartedAt: now, Status: "completed", Transcript: "Тестовый транскрипт", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	runner := New(cfg, st)
	if err := runner.StartStage(meeting.UID, "summarizing"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("summary request did not start")
	}
	if !runner.Cancel(meeting.UID) {
		t.Fatal("active pipeline was not canceled")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runner.IsRunning(meeting.UID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "canceled" || got.ProcessingStage != "canceled" || got.SummaryStatus != "canceled" {
		t.Fatalf("unexpected canceled meeting state: %+v", got)
	}
}

func TestFailedSummaryRetryPreservesLastSuccessfulResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Summary.AutoRun = true
	cfg.Summary.BaseURL = server.URL
	cfg.Summary.Model = "test-model"
	cfg.Summary.MaxRetries = 0
	cfg.Summary.TimeoutSeconds = 5
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "01999999-9999-7999-8999-999999999991", Title: "Summary", StartedAt: now, Status: "completed", Transcript: "Новый транскрипт", Summary: "Последний успешный протокол", SummaryStatus: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := New(cfg, st).retrySummarization(context.Background(), meeting.UID); err == nil {
		t.Fatal("expected summary error")
	}
	got, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != meeting.Summary {
		t.Fatalf("last successful summary was lost: %q", got.Summary)
	}
	if got.SummaryStatus != "failed" {
		t.Fatalf("expected failed retry status, got %q", got.SummaryStatus)
	}
}

func TestPipelineStopsAfterDiarizationFailureAndKeepsSources(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "mock"
	cfg.Diarization.AutoRun = true
	cfg.Diarization.Engine = "sherpa-onnx-cli"
	cfg.Diarization.Command = filepath.Join(dir, "missing-sherpa")
	cfg.Diarization.SegmentationModel = filepath.Join(dir, "missing-segmentation.onnx")
	cfg.Diarization.EmbeddingModel = filepath.Join(dir, "missing-embedding.onnx")

	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	m := model.Meeting{UID: "01999999-9999-7999-8999-999999999998", Title: "Fallback", StartedAt: started, Status: "processing", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	createTestWAVs(t, filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio"))

	if err := New(cfg, st).Run(context.Background(), m.UID); err == nil {
		t.Fatal("expected diarization error to stop the automatic chain")
	}
	got, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("diarization failure should stop the chain: %+v", got)
	}
	if got.LastError == "" {
		t.Fatal("expected diarization error")
	}
	jobs, err := st.Jobs(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	foundFailure := false
	foundCompletedTranscription := false
	for _, job := range jobs {
		if job.Stage == "diarizing" && job.Status == "failed" {
			foundFailure = true
		}
		if job.Stage == "transcribing" && job.Status == "completed" && job.Progress == 100 {
			foundCompletedTranscription = true
		}
	}
	if !foundFailure {
		t.Fatal("failed diarization job is missing")
	}
	if !foundCompletedTranscription {
		t.Fatal("transcription must be completed before diarization failure")
	}
	for _, name := range []string{"microphone.wav", "system.wav"} {
		if _, err := os.Stat(filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio", name)); err != nil {
			t.Fatalf("source %s must survive processing warning: %v", name, err)
		}
	}
	artifacts, err := st.Artifacts(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Type == "mixed_opus" {
			t.Fatal("audio must not be compressed while a stage has failed")
		}
	}
	retryConfig := cfg
	retryConfig.Diarization.Engine = "mock"
	if err := New(retryConfig, st).retryDiarization(context.Background(), m.UID); err != nil {
		t.Fatalf("stage-only diarization retry failed: %v", err)
	}
	retried, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "completed" || retried.Transcript == "" {
		t.Fatalf("stage-only retry did not rebuild the transcript: %+v", retried)
	}
}

func TestPipelineForcedMixedTranscription(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "mock"
	cfg.Transcription.SourceMode = "mixed"
	cfg.Diarization.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	m := model.Meeting{UID: "01999999-9999-7999-8999-999999999997", Title: "Mixed", StartedAt: started, Status: "processing", CreatedAt: started, UpdatedAt: started}
	if err := st.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	createTestWAVs(t, filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio"))
	if err := New(cfg, st).Run(context.Background(), m.UID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || !strings.Contains(got.Transcript, "Test fragment mixed") {
		t.Fatalf("unexpected mixed result: %+v", got)
	}
	artifacts, err := st.Artifacts(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	foundMixedTranscript := false
	for _, artifact := range artifacts {
		if artifact.Type == "system_transcript_json" {
			t.Fatal("forced mixed mode must skip system transcription")
		}
		if artifact.Type == "mixed_transcript_json" {
			foundMixedTranscript = true
		}
	}
	if !foundMixedTranscript {
		t.Fatal("mixed transcript artifact is missing")
	}
}

func TestPipelineImportedSeparateSourcesUsesBoth(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.App.DataDir = dir
	cfg.Storage.DatabasePath = filepath.Join(dir, "database", "meetings.db")
	cfg.Storage.AudioAfterProcessing = "wav"
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "mock"
	cfg.Diarization.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	m := model.Meeting{
		UID:       "01999999-9999-7999-8999-999999999996",
		Title:     "Separate import",
		StartedAt: started,
		Status:    "processing",
		Source:    "imported",
		CreatedAt: started,
		UpdatedAt: started,
		Metadata: map[string]string{
			"audio_import_sources":      "microphone,system",
			"transcription_source_mode": "separate",
		},
	}
	if err := st.SaveMeeting(m); err != nil {
		t.Fatal(err)
	}
	createTestWAVs(t, filepath.Join(dir, "meetings", "2026", "09", m.UID, "audio"))
	if err := New(cfg, st).Run(context.Background(), m.UID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Meeting(m.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Transcript, "Test fragment microphone") || !strings.Contains(got.Transcript, "Test fragment system") {
		t.Fatalf("both imported sources must be transcribed: %q", got.Transcript)
	}
}

func createTestWAVs(t *testing.T, meetingDir string) {
	t.Helper()
	for _, name := range []string{"microphone.wav", "system.wav"} {
		w, err := audio.NewWAVWriter(filepath.Join(meetingDir, name), 16000, 1)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]byte, 3200)
		for i := 0; i < len(pcm); i += 2 {
			binary.LittleEndian.PutUint16(pcm[i:i+2], uint16(int16(100)))
		}
		if err := w.WritePCM16(pcm); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
