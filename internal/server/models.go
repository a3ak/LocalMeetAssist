package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"localmeetassist/internal/config"
)

// modelList returns the state of all known models and runtimes.
func (s *Server) modelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, s.models.Statuses())
}

// modelPath routes model download, apply and test actions.
func (s *Server) modelPath(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/models/"))
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "recommended" {
		s.downloadRecommended(w)
		return
	}
	if len(parts) == 1 && parts[0] == "delete-all" {
		s.deleteAllModels(w)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "download":
		if err := s.models.Start(parts[0]); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		// Скачали модели для функции — включаем и сам шаг обработки, чтобы не
		// искать нужную настройку вручную.
		if err := s.enableStepsForModels(parts[0]); err != nil {
			s.logger.Printf("enable step after model download id=%s: %v", parts[0], err)
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "downloading"})
	case "apply":
		s.applyModel(w, parts[0])
	case "test":
		result := s.models.Test(parts[0])
		writeJSON(w, http.StatusOK, result)
	case "delete":
		s.deleteModel(w, parts[0])
	default:
		http.NotFound(w, r)
	}
}

// applyModel activates an installed model and persists the new configuration.
func (s *Server) applyModel(w http.ResponseWriter, id string) {
	if s.pipeline.AnyRunning() || s.recorder.AnyActive() {
		writeError(w, http.StatusConflict, "wait for recording and processing to finish before changing the model")
		return
	}
	if s.models.AnyDownloading() {
		writeError(w, http.StatusConflict, "wait for the model download to finish")
		return
	}
	spec, err := s.models.ApplyTarget(id)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	current := s.config()
	value := portableConfigPath(current.ConfigFile, spec.Path)
	updates := map[string]string{spec.SettingKey: strconv.Quote(value)}
	if strings.TrimSpace(spec.Engine) != "" {
		updates["transcription.engine"] = strconv.Quote(spec.Engine)
	}
	if err := s.persistSettings(updates); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Printf("model applied id=%s setting=%s path=%q", id, spec.SettingKey, value)
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied", "setting": spec.SettingKey, "path": value})
}

// downloadRecommended starts downloads for every recommended model that is not
// installed yet and switches on the pipeline steps those models serve.
func (s *Server) downloadRecommended(w http.ResponseWriter) {
	ids := make([]string, 0)
	for _, status := range s.models.Statuses() {
		if status.Recommended && !status.Exists && !status.Downloading {
			ids = append(ids, status.ID)
		}
	}
	for _, id := range ids {
		if err := s.models.Start(id); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if len(ids) > 0 {
		if err := s.enableStepsForModels(ids...); err != nil {
			s.logger.Printf("enable steps after recommended download: %v", err)
		}
	}
	s.logger.Printf("recommended models requested count=%d", len(ids))
	writeJSON(w, http.StatusAccepted, map[string]any{"started": ids})
}

// deleteModel removes one installed model so the disk space can be reclaimed.
func (s *Server) deleteModel(w http.ResponseWriter, id string) {
	if s.pipeline.AnyRunning() || s.recorder.AnyActive() {
		writeError(w, http.StatusConflict, "wait for recording and processing to finish before deleting a model")
		return
	}
	if s.models.AnyDownloading() {
		writeError(w, http.StatusConflict, "wait for the model download to finish")
		return
	}
	if err := s.models.Delete(id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Printf("model deleted id=%s", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// deleteAllModels removes every installed model that is not currently applied.
func (s *Server) deleteAllModels(w http.ResponseWriter) {
	if s.pipeline.AnyRunning() || s.recorder.AnyActive() {
		writeError(w, http.StatusConflict, "wait for recording and processing to finish before deleting models")
		return
	}
	if s.models.AnyDownloading() {
		writeError(w, http.StatusConflict, "wait for the model download to finish")
		return
	}
	removed, err := s.models.DeleteAll()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Printf("models deleted count=%d", removed)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// enableStepsForModels turns on the pipeline step a model group belongs to:
// installing models for a feature also switches the feature on.
func (s *Server) enableStepsForModels(ids ...string) error {
	updates := map[string]string{}
	for _, status := range s.models.Statuses() {
		if !slices.Contains(ids, status.ID) {
			continue
		}
		switch status.Group {
		case "transcription", "speech_filter":
			updates["transcription.auto_run"] = "true"
		case "diarization":
			updates["diarization.auto_run"] = "true"
		case "summary":
			updates["summary.auto_run"] = "true"
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return s.persistSettings(updates)
}

// persistSettings writes config keys and reloads the running configuration.
func (s *Server) persistSettings(updates map[string]string) error {
	current := s.config()
	if strings.TrimSpace(current.ConfigFile) == "" {
		return errors.New("the path to config.toml is not set")
	}
	if err := config.UpdateFile(current.ConfigFile, updates); err != nil {
		return err
	}
	loaded, err := config.Load(current.ConfigFile)
	if err != nil {
		return fmt.Errorf("the configuration could not be reloaded: %w", err)
	}
	loaded.ConfigFile = current.ConfigFile
	config.ResolvePaths(&loaded, current.ConfigFile)
	// Ресурсы ниже принадлежат текущему процессу и меняются только после перезапуска.
	loaded.App = current.App
	loaded.Storage.DatabasePath = current.Storage.DatabasePath
	loaded.Storage.BackupCount = current.Storage.BackupCount
	loaded.Logging = current.Logging
	if err := s.pipeline.UpdateConfig(loaded); err != nil {
		return err
	}
	if err := s.models.UpdateConfig(loaded); err != nil {
		return err
	}
	s.setConfig(loaded)
	return nil
}

// portableConfigPath makes a path relative to the config file when possible.
func portableConfigPath(configPath, target string) string {
	relative, err := filepath.Rel(filepath.Dir(configPath), target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return target
	}
	if relative == "." || strings.HasPrefix(relative, "."+string(filepath.Separator)) {
		return relative
	}
	return "." + string(filepath.Separator) + relative
}
