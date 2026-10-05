package server

import (
	"net/http"
	"path/filepath"
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
	if r.Method != http.MethodPost || len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "download":
		if err := s.models.Start(parts[0]); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "downloading"})
	case "apply":
		s.applyModel(w, parts[0])
	case "test":
		result := s.models.Test(parts[0])
		writeJSON(w, http.StatusOK, result)
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
	if strings.TrimSpace(current.ConfigFile) == "" {
		writeError(w, http.StatusConflict, "the path to config.toml is not set")
		return
	}
	value := portableConfigPath(current.ConfigFile, spec.Path)
	updates := map[string]string{spec.SettingKey: strconv.Quote(value)}
	if strings.TrimSpace(spec.Engine) != "" {
		updates["transcription.engine"] = strconv.Quote(spec.Engine)
	}
	if err := config.UpdateFile(current.ConfigFile, updates); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	loaded, err := config.Load(current.ConfigFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the model was selected but the configuration could not be reloaded: "+err.Error())
		return
	}
	loaded.ConfigFile = current.ConfigFile
	config.ResolvePaths(&loaded, current.ConfigFile)
	// Ресурсы ниже принадлежат текущему процессу и меняются только после перезапуска.
	loaded.App = current.App
	loaded.Storage.DatabasePath = current.Storage.DatabasePath
	loaded.Storage.BackupCount = current.Storage.BackupCount
	loaded.Logging = current.Logging
	if err := s.pipeline.UpdateConfig(loaded); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.models.UpdateConfig(loaded); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.setConfig(loaded)
	s.logger.Printf("model applied id=%s setting=%s path=%q", id, spec.SettingKey, value)
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied", "setting": spec.SettingKey, "path": value})
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
