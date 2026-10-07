package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
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
	"localmeetassist/internal/nativeaudio"
	"localmeetassist/internal/recording"
	"localmeetassist/internal/store"
)

func TestMeetingAPIAndCSRF(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()
	body, _ := json.Marshal(map[string]string{"title": "API test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/meetings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte("API test")) {
		t.Fatalf("unexpected list: %d %s", rr.Code, rr.Body.String())
	}
}

func TestMeetingParticipantCountCanBeUpdatedPerMeeting(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-speakers", Title: "Speakers", StartedAt: now, Status: "created", Source: "manual", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	body := bytes.NewBufferString(`{"participant_count":8}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/meetings/meeting-speakers", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["participant_count"] != "8" {
		t.Fatalf("participant count not saved in meeting metadata: %+v", updated.Metadata)
	}
}

func TestRenameSpeakerUpdatesMeetingAndTranscriptArtifact(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{
		UID:        "meeting-speaker-rename",
		Title:      "Rename",
		StartedAt:  now,
		Status:     "completed",
		Transcript: "[00:00] SPEAKER_01: Привет\n[00:05] SPEAKER_02: SPEAKER_01 уже ответил",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	// Reproduce a meeting renamed by an older LocalMeetAssist build: the speaker
	// record already contains the chosen name, while the transcript still has
	// the technical ID.
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_01", DisplayName: "Заур", Source: "system"}); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "transcript", "transcript.md")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	fileText := "---\nmeeting_uid: " + meeting.UID + "\n---\n\n" + meeting.Transcript + "\n"
	if err := os.WriteFile(transcriptPath, []byte(fileText), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := model.Artifact{
		UID:        "speaker-rename-transcript",
		MeetingUID: meeting.UID,
		Type:       "transcript",
		Path:       transcriptPath,
		MIMEType:   "text/markdown",
		SizeBytes:  int64(len(fileText)),
		CreatedAt:  now,
	}
	if err := st.SaveArtifact(artifact); err != nil {
		t.Fatal(err)
	}

	s := New(cfg, st, log.New(io.Discard, "", 0))
	body := bytes.NewBufferString(`{"display_name":"Заур"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/meetings/"+meeting.UID+"/speakers/SPEAKER_01", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", rr.Code, rr.Body.String())
	}

	updatedMeeting, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	wantTranscript := "[00:00] Заур: Привет\n[00:05] SPEAKER_02: SPEAKER_01 уже ответил"
	if updatedMeeting.Transcript != wantTranscript {
		t.Fatalf("unexpected meeting transcript:\n%s", updatedMeeting.Transcript)
	}
	updatedFile, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedFile), "[00:00] Заур: Привет") || !strings.Contains(string(updatedFile), "SPEAKER_01 уже ответил") {
		t.Fatalf("unexpected transcript artifact:\n%s", updatedFile)
	}
	updatedArtifact, err := st.Artifact(artifact.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedArtifact.SizeBytes != int64(len(updatedFile)) || updatedArtifact.SHA256 == "" {
		t.Fatalf("artifact metadata was not refreshed: %+v", updatedArtifact)
	}
}

func TestBatchSpeakerEditsMoveMergeAndRenameAtomically(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-speaker-batch", Title: "Batch", StartedAt: now, Status: "completed", Transcript: "old", Summary: "Старый протокол", SummaryStatus: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	for _, speaker := range []model.Speaker{
		{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "SPEAKER_00", Source: "system"},
		{MeetingUID: meeting.UID, ID: "SPEAKER_01", DisplayName: "SPEAKER_01", Source: "system"},
		{MeetingUID: meeting.UID, ID: "SPEAKER_02", DisplayName: "SPEAKER_02", Source: "system"},
	} {
		if err := st.SaveSpeaker(speaker); err != nil {
			t.Fatal(err)
		}
	}
	segments := []model.Segment{
		{ID: "fragment-1", StartMS: 0, EndMS: 1000, SpeakerID: "SPEAKER_00", Source: "system", Text: "Первая"},
		{ID: "fragment-2", StartMS: 2000, EndMS: 3000, SpeakerID: "SPEAKER_01", Source: "system", Text: "Вторая"},
		{ID: "fragment-3", StartMS: 4000, EndMS: 5000, SpeakerID: "SPEAKER_02", Source: "system", Text: "Третья"},
	}
	if err := st.SaveTranscriptSegments(meeting.UID, segments); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	body := bytes.NewBufferString(`{"names":{"SPEAKER_00":"Антон","SPEAKER_01":"Борис","SPEAKER_02":"Виктор"},"assignments":{"fragment-2":"SPEAKER_00"},"merges":{"SPEAKER_02":"SPEAKER_00"}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/meetings/"+meeting.UID+"/speakers", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("batch speaker update failed: %d %s", rr.Code, rr.Body.String())
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(updated.Transcript, "Антон:") != 3 || updated.Metadata["summary_stale"] != "true" || updated.Summary != meeting.Summary {
		t.Fatalf("unexpected promoted meeting: %+v", updated)
	}
	speakers, err := st.Speakers(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(speakers) != 1 || speakers[0].ID != "SPEAKER_00" || len(speakers[0].Fragments) != 3 {
		t.Fatalf("unexpected speakers after merge: %+v", speakers)
	}
}

func TestSummaryConnectionCheckUsesUnsavedSettings(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer llm.Close()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	payload, _ := json.Marshal(map[string]any{"group": "summary", "values": map[string]string{
		"summary.base_url": llm.URL + "/v1",
		"summary.model":    "test-model",
		"summary.token":    "test-token",
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"ok":true`)) {
		t.Fatalf("summary connection check failed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestCancelProcessingEndpointRejectsInactiveMeeting(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-cancel", Title: "Cancel", StartedAt: now, Status: "created", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/meeting-cancel/jobs/cancel", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for inactive pipeline, got %d: %s", rr.Code, rr.Body.String())
	}
	job := model.Job{UID: "job-progress", MeetingUID: meeting.UID, Stage: "transcribing", Status: "processing", Progress: 42, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/meetings/meeting-cancel/jobs", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"progress":42`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"processing_active":false`)) {
		t.Fatalf("unexpected lightweight job status: %d %s", rr.Code, rr.Body.String())
	}
}

func TestEmptyCalendarAndRemovedSessionEndpoint(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("empty calendar must be [], got %d %q", rr.Code, rr.Body.String())
	}
	// The session token is no longer handed out over HTTP: even with a valid
	// session token the route is gone.
	rr = httptest.NewRecorder()
	sessReq := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	sessReq.Header.Set("X-Meeting-Token", s.token)
	h.ServeHTTP(rr, sessReq)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("session endpoint must be gone, got %d", rr.Code)
	}
}

func TestFaviconIsServedAndIndexReferencesIt(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(cfg, st, log.New(io.Discard, "", 0)).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("favicon must be served, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("unexpected favicon content type: %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "<svg") {
		t.Fatalf("favicon body is not SVG: %q", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Body.String(), "/web/favicon.svg") {
		t.Fatal("index must reference the favicon")
	}
}

func TestSettingsAPIUpdatesConfigAndHidesToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configText := "config_version = 1\n[app]\ndata_dir = \"./data\"\n[summary]\ntoken = \"secret-value\"\n"
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	h := s.Handler()

	rr := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	getReq.Header.Set("X-Meeting-Token", s.token)
	h.ServeHTTP(rr, getReq)
	if rr.Code != http.StatusOK || bytes.Contains(rr.Body.Bytes(), []byte("secret-value")) || !bytes.Contains(rr.Body.Bytes(), []byte("secret_set")) {
		t.Fatalf("unexpected settings response: %d %s", rr.Code, rr.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"values": map[string]string{
		"storage.audio_after_processing": "opus",
		"storage.opus_bitrate_kbps":      "48",
		"transcription.source_mode":      "auto",
		"transcription.model_path":       "./models/whisper/ggml-medium.bin",
		"summary.system_prompt":          "Ты создаёшь проверяемый протокол.\nНе выдумывай факты.",
	}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"restart_required":false`)) {
		t.Fatalf("settings update failed: %d %s", rr.Code, rr.Body.String())
	}
	if runtimeConfig := s.config(); runtimeConfig.Storage.OpusBitrateKbps != 48 || runtimeConfig.Transcription.SourceMode != "auto" || filepath.Base(runtimeConfig.Transcription.ModelPath) != "ggml-medium.bin" || !strings.Contains(runtimeConfig.Summary.SystemPrompt, "Не выдумывай") {
		t.Fatalf("settings were not hot-applied: %+v", runtimeConfig)
	}
	updated, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Storage.AudioAfterProcessing != "opus" || updated.Storage.OpusBitrateKbps != 48 || updated.Summary.Token != "secret-value" || updated.Transcription.ModelPath != "./models/whisper/ggml-medium.bin" || !strings.Contains(updated.Summary.SystemPrompt, "Не выдумывай") {
		t.Fatalf("unexpected persisted settings: %+v", updated)
	}
}

func TestApplyDownloadedModelUpdatesConfigAndRuntime(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configText := "config_version = 1\n[app]\ndata_dir = \"./data\"\n[transcription]\nmodel_path = \"./models/asr/gigaam\"\n"
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	mediumPath := filepath.Join(dir, "models", "asr", "gigaam")
	if err := os.MkdirAll(mediumPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v3_e2e_rnnt_encoder.int8.onnx", "v3_e2e_rnnt_decoder.int8.onnx", "v3_e2e_rnnt_joint.int8.onnx", "v3_e2e_rnnt_vocab.txt"} {
		if err := os.WriteFile(filepath.Join(mediumPath, name), []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/gigaam-v3-e2e-rnnt-int8/apply", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("apply model failed: %d %s", rr.Code, rr.Body.String())
	}
	if got := filepath.Clean(s.config().Transcription.ModelPath); got != filepath.Clean(mediumPath) {
		t.Fatalf("runtime model not changed: %q", got)
	}
	persisted, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Transcription.ModelPath != "./models/asr/gigaam" {
		t.Fatalf("portable model path not persisted: %q", persisted.Transcription.ModelPath)
	}
}

func TestSettingsAPIReportsOnlyProcessOwnedFieldsAsRestartRequired(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("config_version = 1\n[app]\ndata_dir = \"./data\"\nlisten_port = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConfigFile = configPath
	config.ResolvePaths(&cfg, configPath)
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log.New(io.Discard, "", 0))
	body, _ := json.Marshal(map[string]any{"values": map[string]string{"app.listen_port": "9123"}})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"restart_required":true`)) || !bytes.Contains(rr.Body.Bytes(), []byte("app.listen_port")) {
		t.Fatalf("unexpected static-setting response: %d %s", rr.Code, rr.Body.String())
	}
	if s.config().App.ListenPort != 0 {
		t.Fatal("running listener configuration must not change")
	}
	persisted, err := config.Load(configPath)
	if err != nil || persisted.App.ListenPort != 9123 {
		t.Fatalf("static setting was not persisted: %+v err=%v", persisted, err)
	}
}

func TestRecordingCalendarEndToEnd(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Audio.InputDeviceID = "mic"
	cfg.Audio.InputDeviceName = "Test microphone"
	cfg.Audio.OutputDeviceID = "system"
	cfg.Audio.OutputDeviceName = "Test system"
	cfg.Transcription.AutoRun = false
	cfg.Summary.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := log.New(io.Discard, "", 0)
	recorder := recording.NewManagerWithBackend(cfg.Audio, logger, serverSyntheticBackend{})
	s := NewWithRecorder(cfg, st, logger, recorder)
	h := s.Handler()

	startBody, _ := json.Marshal(map[string]any{
		"title":         "Synthetic recording",
		"input_device":  map[string]string{"id": cfg.Audio.InputDeviceID, "name": cfg.Audio.InputDeviceName},
		"output_device": map[string]string{"id": cfg.Audio.OutputDeviceID, "name": cfg.Audio.OutputDeviceName},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader(startBody))
	req.Header.Set("X-Meeting-Token", s.token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start failed: %d %s", rr.Code, rr.Body.String())
	}
	var started struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.UID == "" {
		t.Fatalf("invalid start response: %v %s", err, rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/recordings", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"active":true`)) || !bytes.Contains(rr.Body.Bytes(), []byte(started.UID)) {
		t.Fatalf("active recording state was not published: %d %s", rr.Code, rr.Body.String())
	}
	time.Sleep(600 * time.Millisecond)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/recordings/"+started.UID+"/stop", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("stop failed: %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/recordings", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !bytes.Contains(rr.Body.Bytes(), []byte(`"active":false`)) {
		t.Fatalf("recording state was not cleared: %d %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		meeting, err := st.Meeting(started.UID)
		if err == nil && meeting.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("meeting was not completed: %+v err=%v", meeting, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	items, err := st.ListMeetings(nil, nil)
	if err != nil || len(items) != 1 || items[0].UID != started.UID {
		t.Fatalf("calendar list mismatch: %+v err=%v", items, err)
	}
	artifacts, err := st.Artifacts(started.UID)
	if err != nil {
		t.Fatal(err)
	}
	foundMixed := false
	for _, artifact := range artifacts {
		if artifact.Type == "mixed_wav" {
			foundMixed = true
			info, statErr := os.Stat(artifact.Path)
			if statErr != nil || info.Size() <= 64 {
				t.Fatalf("mixed WAV invalid: info=%v err=%v", info, statErr)
			}
		}
	}
	if !foundMixed {
		t.Fatalf("mixed WAV is missing: %+v", artifacts)
	}
}

func TestRecoverProcessingAfterRestart(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Transcription.AutoRun = false
	cfg.Diarization.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	startedAt := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Minute)
	meeting := model.Meeting{UID: "01999999-aaaa-7aaa-8aaa-999999999999", Title: "Interrupted", StartedAt: startedAt, FinishedAt: &finishedAt, Status: "processing", ProcessingStage: "transcribing", CreatedAt: startedAt, UpdatedAt: finishedAt}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	audioDir := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "audio")
	for _, name := range []string{"microphone.wav", "system.wav"} {
		writer, createErr := audio.NewWAVWriter(filepath.Join(audioDir, name), 16000, 1)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = writer.WritePCM16(make([]byte, 640)); createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = writer.Close(); createErr != nil {
			t.Fatal(createErr)
		}
	}
	s := New(cfg, st, log.New(io.Discard, "", 0))
	if got := s.RecoverProcessing(); got != 1 {
		t.Fatalf("expected one recovered pipeline, got %d", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, loadErr := st.Meeting(meeting.UID)
		if loadErr == nil && got.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered pipeline did not complete: %+v err=%v", got, loadErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeleteArtifactRemovesFileAndDatabaseRecord(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "01999999-bbbb-7bbb-8bbb-999999999999", Title: "Completed", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "audio", "extra.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := model.Artifact{UID: "01999999-cccc-7ccc-8ccc-999999999999", MeetingUID: meeting.UID, Type: "extra", Path: path, MIMEType: "audio/wav", CreatedAt: now}
	if err := st.SaveArtifact(artifact); err != nil {
		t.Fatal(err)
	}

	s := New(cfg, st, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/meetings/"+meeting.UID+"/artifacts/"+artifact.UID, nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact file was not deleted: %v", err)
	}
	if _, err := st.Artifact(artifact.UID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact record was not deleted: %v", err)
	}
}

func TestArtifactDownloadDeclaresUTF8(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	meeting := model.Meeting{UID: "01999999-abcd-7abc-8abc-999999999999", Title: "UTF-8", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.App.DataDir, "meetings", now.Format("2006"), now.Format("01"), meeting.UID, "transcript", "transcript.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Владелец микрофона: Слышно?"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := model.Artifact{UID: "01999999-abce-7abc-8abc-999999999999", MeetingUID: meeting.UID, Type: "transcript", Path: path, MIMEType: "text/markdown", CreatedAt: now}
	if err := st.SaveArtifact(artifact); err != nil {
		t.Fatal(err)
	}

	s := New(cfg, st, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meetings/"+meeting.UID+"/artifacts/"+artifact.UID, nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("download failed: %d %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("unexpected Content-Type: %q", got)
	}
	if rr.Body.String() != "Владелец микрофона: Слышно?" {
		t.Fatalf("UTF-8 body was corrupted: %q", rr.Body.String())
	}
}

func TestImportSystemAudioAndRetryPipeline(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	cfg.Storage.AudioAfterProcessing = "wav"
	cfg.Transcription.AutoRun = true
	cfg.Transcription.Engine = "mock"
	cfg.Transcription.SourceMode = "auto"
	cfg.Diarization.AutoRun = true
	cfg.Diarization.Engine = "mock"
	cfg.Summary.AutoRun = false
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 24, 14, 45, 0, 0, time.UTC)
	meeting := model.Meeting{UID: "01999999-dddd-7ddd-8ddd-999999999999", Title: "Imported system", StartedAt: now, Status: "created", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}

	uploadPath := filepath.Join(t.TempDir(), "system.wav")
	writer, err := audio.NewWAVWriter(uploadPath, 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 32000)
	for offset := 0; offset < len(pcm); offset += 2 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(1800)))
	}
	if err := writer.WritePCM16(pcm); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploadData, err := os.ReadFile(uploadPath)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "system.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(uploadData); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("type", "audio_system"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	s := New(cfg, st, log.New(io.Discard, "", 0))
	handler := s.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/"+meeting.UID+"/artifacts", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("X-Meeting-Token", s.token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("audio import failed: %d %s", rr.Code, rr.Body.String())
	}

	imported, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Metadata["audio_import_type"] != "system" || imported.Metadata["audio_import_sources"] != "system" || imported.Metadata["transcription_source_mode"] != "separate" {
		t.Fatalf("unexpected import metadata: %+v", imported.Metadata)
	}
	audioDir := filepath.Join(cfg.App.DataDir, "meetings", "2026", "09", meeting.UID, "audio")
	if _, err := os.Stat(filepath.Join(audioDir, "system.wav")); err != nil {
		t.Fatalf("canonical system source is missing: %v", err)
	}
	mic, err := os.Open(filepath.Join(audioDir, "microphone.wav"))
	if err != nil {
		t.Fatalf("silent microphone companion is missing: %v", err)
	}
	micInfo, err := audio.ReadWAVInfo(mic)
	mic.Close()
	if err != nil || micInfo.DataSize != int64(len(pcm)) {
		t.Fatalf("invalid silent companion: %+v err=%v", micInfo, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/meetings/"+meeting.UID+"/jobs/all/retry", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry failed: %d %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		processed, loadErr := st.Meeting(meeting.UID)
		if loadErr == nil && (processed.Status == "completed" || processed.Status == "warning") {
			if !strings.Contains(processed.Transcript, "Test fragment system") || strings.Contains(processed.Transcript, "mixed") || strings.Contains(processed.Transcript, "microphone") {
				t.Fatalf("system import must be transcribed exactly once from system source: %q", processed.Transcript)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("imported pipeline did not finish: %+v err=%v", processed, loadErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	artifacts, err := st.Artifacts(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Type == "microphone_transcript_json" || artifact.Type == "mixed_transcript_json" {
			t.Fatalf("unexpected redundant transcript artifact: %+v", artifact)
		}
	}
}

type serverSyntheticBackend struct{}

func (serverSyntheticBackend) Name() string { return "synthetic-native-test" }
func (serverSyntheticBackend) Devices() ([]nativeaudio.Device, []nativeaudio.Device, error) {
	return []nativeaudio.Device{{ID: "mic", Name: "Test microphone", IsDefault: true}},
		[]nativeaudio.Device{{ID: "system", Name: "Test system", IsDefault: true}}, nil
}
func (serverSyntheticBackend) Start(request nativeaudio.StartRequest) (nativeaudio.Session, error) {
	for index, path := range []string{request.MicrophonePath, request.SystemPath} {
		writer, err := audio.NewWAVWriter(path, request.SampleRate, request.Channels)
		if err != nil {
			return nil, err
		}
		pcm := make([]byte, request.SampleRate*request.Channels/2)
		value := int16(1000 + index*1000)
		for offset := 0; offset < len(pcm); offset += 2 {
			binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(value))
		}
		if err := writer.WritePCM16(pcm); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	}
	return noopNativeSession{}, nil
}

type noopNativeSession struct{}

func (noopNativeSession) Stop() error { return nil }
