package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/model"
)

// people lists, renames and deletes known speakers. The global library is
// deliberately separate from the per-meeting speaker cards.
func (s *Server) people(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		views, err := s.peopleViews()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, views)
	case http.MethodDelete:
		if s.pipeline != nil && s.pipeline.AnyRunning() {
			writeError(w, http.StatusConflict, "speakers cannot be changed while processing")
			return
		}
		deleted, err := s.deleteAllPeople()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		methodNotAllowed(w)
	}
}

// personPath routes /api/v1/people/{id}[/samples/{sample_id}[/audio]].
func (s *Server) personPath(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/people/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		s.personCRUD(w, r, id)
		return
	}
	if parts[1] != "samples" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 {
		s.personSample(w, r, id, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "audio" {
		s.personSampleAudio(w, r, id, parts[2])
		return
	}
	http.NotFound(w, r)
}

func (s *Server) personCRUD(w http.ResponseWriter, r *http.Request, id string) {
	person, err := s.store.Person(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "speaker not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, person)
	case http.MethodPatch:
		if s.pipeline != nil && s.pipeline.AnyRunning() {
			writeError(w, http.StatusConflict, "speakers cannot be changed while processing")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "the speaker name cannot be empty")
			return
		}
		person.Name = name
		person.NeedsQualifier = false
		person.UpdatedAt = time.Now().UTC()
		if err := s.store.SavePerson(person); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, person)
	case http.MethodDelete:
		if s.pipeline != nil && s.pipeline.AnyRunning() {
			writeError(w, http.StatusConflict, "speakers cannot be changed while processing")
			return
		}
		if err := s.deletePerson(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) personSample(w http.ResponseWriter, r *http.Request, personID, sampleID string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	if s.pipeline != nil && s.pipeline.AnyRunning() {
		writeError(w, http.StatusConflict, "speakers cannot be changed while processing")
		return
	}
	if _, err := s.store.Person(personID); err != nil {
		writeError(w, http.StatusNotFound, "speaker not found")
		return
	}
	samples, err := s.store.VoiceSamples(personID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var target *model.VoiceSample
	for index := range samples {
		if samples[index].ID == sampleID {
			target = &samples[index]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "voice sample not found")
		return
	}
	if err := s.store.DeleteVoiceSample(personID, sampleID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.removeVoiceClip(target.ClipPath)
	remaining, _ := s.store.VoiceSamples(personID)
	personDeleted := false
	if len(remaining) == 0 {
		if err := s.store.DeletePerson(personID); err == nil {
			_ = s.store.UnlinkPerson(personID)
			if dir := filepath.Join(s.config().App.DataDir, "voices", personID); s.safeManagedPath(dir) {
				_ = os.RemoveAll(dir)
			}
			personDeleted = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "person_deleted": personDeleted})
}

func (s *Server) personSampleAudio(w http.ResponseWriter, r *http.Request, personID, sampleID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	if requestAuth(r).scope == scopeReadOnly {
		writeError(w, http.StatusForbidden, "voice samples are not available to read-only tokens")
		return
	}
	samples, err := s.store.VoiceSamples(personID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, sample := range samples {
		if sample.ID != sampleID {
			continue
		}
		if sample.ClipPath == "" || !s.safeManagedPath(sample.ClipPath) {
			writeError(w, http.StatusNotFound, "voice clip not found")
			return
		}
		if info, statErr := os.Stat(sample.ClipPath); statErr != nil || info.Size() == 0 {
			writeError(w, http.StatusNotFound, "voice clip not found")
			return
		}
		w.Header().Set("Content-Type", "audio/ogg")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, sample.ClipPath)
		return
	}
	writeError(w, http.StatusNotFound, "voice sample not found")
}

func (s *Server) peopleViews() ([]model.PersonView, error) {
	people, err := s.store.People()
	if err != nil {
		return nil, err
	}
	all, err := s.store.AllVoiceSamples()
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]model.VoiceSample)
	for _, sample := range all {
		grouped[sample.PersonID] = append(grouped[sample.PersonID], sample)
	}
	views := make([]model.PersonView, 0, len(people))
	for _, person := range people {
		views = append(views, s.personView(person, grouped[person.ID]))
	}
	sort.Slice(views, func(i, j int) bool {
		if strings.EqualFold(views[i].Name, views[j].Name) {
			return views[i].ID < views[j].ID
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	return views, nil
}

func (s *Server) personView(person model.Person, samples []model.VoiceSample) model.PersonView {
	sort.Slice(samples, func(i, j int) bool { return samples[i].CreatedAt.Before(samples[j].CreatedAt) })
	view := model.PersonView{Person: person, Samples: make([]model.VoiceSampleView, 0, len(samples))}
	meetings := make(map[string]bool)
	var last time.Time
	for _, sample := range samples {
		if sample.MeetingUID != "" {
			meetings[sample.MeetingUID] = true
		}
		if sample.CreatedAt.After(last) {
			last = sample.CreatedAt
		}
		view.Samples = append(view.Samples, model.VoiceSampleView{
			ID:         sample.ID,
			MeetingUID: sample.MeetingUID,
			SpeakerID:  sample.SpeakerID,
			Source:     sample.Source,
			StartMS:    sample.StartMS,
			EndMS:      sample.EndMS,
			Quality:    sample.Quality,
			ClipURL:    "/api/v1/people/" + person.ID + "/samples/" + sample.ID + "/audio",
			CreatedAt:  sample.CreatedAt,
		})
	}
	view.MeetingCount = len(meetings)
	if !last.IsZero() {
		lastCopy := last
		view.LastSeen = &lastCopy
	}
	return view
}

func (s *Server) deletePerson(id string) error {
	samples, err := s.store.VoiceSamples(id)
	if err != nil {
		return err
	}
	for _, sample := range samples {
		s.removeVoiceClip(sample.ClipPath)
	}
	if err := s.store.DeletePerson(id); err != nil {
		return err
	}
	if err := s.store.UnlinkPerson(id); err != nil {
		return err
	}
	if dir := filepath.Join(s.config().App.DataDir, "voices", id); s.safeManagedPath(dir) {
		_ = os.RemoveAll(dir)
	}
	return nil
}

func (s *Server) deleteAllPeople() (int, error) {
	people, err := s.store.People()
	if err != nil {
		return 0, err
	}
	for _, person := range people {
		if err := s.deletePerson(person.ID); err != nil {
			return 0, err
		}
	}
	if dir := filepath.Join(s.config().App.DataDir, "voices"); s.safeManagedPath(dir) {
		_ = os.RemoveAll(dir)
	}
	return len(people), nil
}

func (s *Server) removeVoiceClip(path string) {
	if path == "" || !s.safeManagedPath(path) {
		return
	}
	_ = os.Remove(path)
}
