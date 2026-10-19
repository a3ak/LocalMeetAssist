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
	"localmeetassist/internal/modelmanager"
	"localmeetassist/internal/sysinfo"
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
	if len(parts) == 1 && parts[0] == "resources" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, sysinfo.Detect(s.config().App.DataDir))
		return
	}
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
		var request struct {
			Force bool `json:"force"`
		}
		// Тело необязательно: без него запрос означает обычную загрузку.
		if r.Body != nil {
			_ = decodeJSON(r, &request)
		}
		if resources, spec, found, short := s.resourceShortfall(parts[0]); found && short && !request.Force {
			s.logger.Printf("model download refused id=%s: insufficient resources", parts[0])
			writeResourceRefusal(w, resources, spec)
			return
		}
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
	// A runtime card stores its base directory; the platform library below it is
	// chosen at load time.
	settingPath := spec.Path
	if strings.TrimSpace(spec.SettingValue) != "" {
		settingPath = spec.SettingValue
	}
	value := portableConfigPath(current.ConfigFile, settingPath)
	updates := map[string]string{spec.SettingKey: strconv.Quote(value)}
	if strings.TrimSpace(spec.Engine) != "" {
		updates[engineSettingKey(spec.Group)] = strconv.Quote(spec.Engine)
	}
	if err := s.persistSettings(updates); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.logger.Printf("model applied id=%s setting=%s path=%q", id, spec.SettingKey, value)
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied", "setting": spec.SettingKey, "path": value})
}

// engineSettingKey returns the configuration key that selects the engine of a
// model group, so applying a model switches its engine together with its file.
func engineSettingKey(group string) string {
	if group == "summarization" {
		return "summary.engine"
	}
	return "transcription.engine"
}

// downloadRecommended starts downloads for every recommended model that is not
// installed yet and switches on the pipeline steps those models serve.
// resourceShortfallFor compares a model requirement with the machine. Kept
// separate from detection so both directions can be tested: on a large machine
// the shortage branch would otherwise never run.
func resourceShortfallFor(resources sysinfo.Resources, spec modelmanager.Spec) bool {
	if spec.MinRAMBytes > 0 && int64(resources.TotalRAMBytes) < spec.MinRAMBytes {
		return true
	}
	return spec.MinDiskBytes > 0 && int64(resources.FreeDiskBytes) < spec.MinDiskBytes
}

// resourceShortfall reports the model requirement when the machine cannot run
// it. found is false when the model is unknown or has no stated requirement.
func (s *Server) resourceShortfall(id string) (sysinfo.Resources, modelmanager.Spec, bool, bool) {
	for _, status := range s.models.Statuses() {
		if status.ID != id {
			continue
		}
		if status.MinRAMBytes <= 0 && status.MinDiskBytes <= 0 {
			return sysinfo.Resources{}, status.Spec, true, false
		}
		resources := sysinfo.Detect(s.config().App.DataDir)
		return resources, status.Spec, true, resourceShortfallFor(resources, status.Spec)
	}
	return sysinfo.Resources{}, modelmanager.Spec{}, false, false
}

// writeResourceRefusal explains that a model cannot run here and lets the UI ask
// the user whether to download it anyway.
func writeResourceRefusal(w http.ResponseWriter, resources sysinfo.Resources, spec modelmanager.Spec) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"code":                "insufficient_resources",
		"model":               spec.ID,
		"name":                spec.Name,
		"required_ram_bytes":  spec.MinRAMBytes,
		"required_disk_bytes": spec.MinDiskBytes,
		"total_ram_bytes":     resources.TotalRAMBytes,
		"free_disk_bytes":     resources.FreeDiskBytes,
	})
}

func (s *Server) downloadRecommended(w http.ResponseWriter) {
	ids := make([]string, 0)
	skipped := make([]map[string]any, 0)
	for _, status := range s.models.Statuses() {
		if !status.Recommended || status.Exists || status.Downloading {
			continue
		}
		// Модель, которой не хватает ресурсов, в набор не попадает: иначе одна
		// кнопка тянула бы то, что на этой машине всё равно не запустится.
		if resources, spec, found, short := s.resourceShortfall(status.ID); found && short {
			skipped = append(skipped, map[string]any{
				"id":                  spec.ID,
				"name":                spec.Name,
				"required_ram_bytes":  spec.MinRAMBytes,
				"required_disk_bytes": spec.MinDiskBytes,
				"total_ram_bytes":     resources.TotalRAMBytes,
				"free_disk_bytes":     resources.FreeDiskBytes,
			})
			s.logger.Printf("recommended model skipped id=%s: insufficient resources", status.ID)
			continue
		}
		ids = append(ids, status.ID)
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
	s.logger.Printf("recommended models requested count=%d skipped=%d", len(ids), len(skipped))
	writeJSON(w, http.StatusAccepted, map[string]any{"started": ids, "skipped": skipped})
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
	current := s.config()
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
		case "summarization":
			updates["summary.auto_run"] = "true"
			// A local minutes model is activated when no network endpoint is
			// configured, otherwise automatic minutes would fail on the first
			// meeting with an empty URL. A configured endpoint keeps working and
			// the local model waits for the Apply button.
			if endpointIncomplete(current.Summary) && !current.Summary.LocalSummary() {
				updates["summary.engine"] = strconv.Quote(config.EngineLocalLlama)
				updates["summary.model_path"] = strconv.Quote(portableConfigPath(current.ConfigFile, status.Path))
			}
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return s.persistSettings(updates)
}

// endpointIncomplete reports whether the network endpoint still misses a URL or
// a model name, which makes it unusable for automatic minutes.
func endpointIncomplete(summary config.Summary) bool {
	return strings.TrimSpace(summary.BaseURL) == "" || strings.TrimSpace(summary.Model) == ""
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
