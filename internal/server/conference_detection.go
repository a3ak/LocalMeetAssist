package server

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/nativeaudio"
	"localmeetassist/internal/uuidv7"
)

type conferenceActivityProvider interface {
	Activity() nativeaudio.Activity
}

type conferenceCandidate struct {
	ID         string    `json:"id"`
	DetectedAt time.Time `json:"detected_at"`
	Backend    string    `json:"backend"`
	// AppName is the localized name of the app capturing the microphone, when
	// the platform can attribute it. Empty when unknown.
	AppName string `json:"app_name,omitempty"`
	// Trusted means the app is known and may start a recording prompt; an
	// unknown app asks to be remembered first.
	Trusted bool `json:"trusted"`
}

type conferenceDetectionState struct {
	mu          sync.Mutex
	activeSince time.Time
	candidate   *conferenceCandidate
	dismissed   bool
	last        nativeaudio.Activity
	lastWarning time.Time
}

// StartConferenceDetection starts the low-frequency OS audio-session monitor.
// It is intentionally independent of the Web UI: an SSE event updates open
// pages, while the tray polls the same candidate endpoint.
func (s *Server) StartConferenceDetection(ctx context.Context) {
	go func() {
		// Опрос раз в две секунды: окно срабатывания (по умолчанию 10 с) этого не
		// требует, зато на Linux это вдвое меньше запусков pactl в секунду.
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.observeConferenceActivity(now)
			}
		}
	}()
}

func (s *Server) observeConferenceActivity(now time.Time) {
	cfg := s.config().ConferenceDetection
	if !cfg.Enabled || s.recorder.AnyActive() || s.browserAwaitingConfirmation() {
		s.resetConferenceDetection(true)
		return
	}
	activity := s.activity.Activity()
	state := s.conference
	state.mu.Lock()
	state.last = activity
	if !activity.Available {
		hadCandidate := state.candidate != nil
		warn := activity.Warning != "" && (state.lastWarning.IsZero() || now.Sub(state.lastWarning) >= time.Minute)
		if warn {
			state.lastWarning = now
		}
		state.activeSince = time.Time{}
		state.candidate = nil
		state.dismissed = false
		state.mu.Unlock()
		if hadCandidate {
			s.publishConferenceCandidate(nil)
		}
		if warn {
			s.logger.Printf("conference detection unavailable backend=%q warning=%q", activity.Backend, activity.Warning)
		}
		return
	}
	if !activity.Microphone || !activity.System {
		hadCandidate := state.candidate != nil
		state.activeSince = time.Time{}
		state.candidate = nil
		state.dismissed = false
		state.mu.Unlock()
		if hadCandidate {
			s.publishConferenceCandidate(nil)
		}
		return
	}
	if state.dismissed || state.candidate != nil {
		state.mu.Unlock()
		return
	}
	if state.activeSince.IsZero() {
		state.activeSince = now
		state.mu.Unlock()
		return
	}
	window := time.Duration(cfg.WindowSeconds) * time.Second
	if now.Sub(state.activeSince) < window {
		state.mu.Unlock()
		return
	}
	appName := activity.MicrophoneApp
	trusted := appName == "" || slices.Contains(cfg.TrustedApps, appName)
	if appName != "" && slices.Contains(cfg.ExcludedApps, appName) {
		// Приложение в исключениях — его больше никогда не спрашиваем.
		state.mu.Unlock()
		return
	}
	candidate := &conferenceCandidate{ID: uuidv7.New(), DetectedAt: now, Backend: activity.Backend, AppName: appName, Trusted: trusted}
	state.candidate = candidate
	state.mu.Unlock()
	s.logger.Printf("conference detected candidate=%s backend=%q window=%s app=%q trusted=%v", candidate.ID, candidate.Backend, window, appName, trusted)
	s.publishConferenceCandidate(candidate)
}

func (s *Server) browserAwaitingConfirmation() bool {
	bs := s.browser
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.scenario == scenarioConfirm
}

func (s *Server) resetConferenceDetection(publish bool) {
	state := s.conference
	state.mu.Lock()
	hadCandidate := state.candidate != nil
	state.activeSince = time.Time{}
	state.candidate = nil
	state.dismissed = false
	state.mu.Unlock()
	if publish && hadCandidate {
		s.publishConferenceCandidate(nil)
	}
}

func (s *Server) conferenceSnapshot() map[string]any {
	state := s.conference
	state.mu.Lock()
	defer state.mu.Unlock()
	return map[string]any{"candidate": state.candidate, "activity": state.last}
}

// rememberConferenceApp appends an app to a trusted/excluded list and persists
// the configuration, deduplicating by exact name.
func (s *Server) rememberConferenceApp(key, appName string) error {
	cfg := s.config()
	list := cfg.ConferenceDetection.TrustedApps
	if key == "conference_detection.excluded_apps" {
		list = cfg.ConferenceDetection.ExcludedApps
	}
	if slices.Contains(list, appName) {
		return nil
	}
	list = append(list, appName)
	return s.persistSettings(map[string]string{key: config.QuotedList(list)})
}

func (s *Server) publishConferenceCandidate(candidate *conferenceCandidate) {
	s.publishEvent("conference_detection", map[string]any{"candidate": candidate})
}

func (s *Server) conferenceDetection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.conferenceSnapshot())
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request struct {
		Action      string `json:"action"`
		CandidateID string `json:"candidate_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state := s.conference
	state.mu.Lock()
	candidate := state.candidate
	if candidate == nil || request.CandidateID == "" || request.CandidateID != candidate.ID {
		state.mu.Unlock()
		writeError(w, http.StatusConflict, "conference candidate is no longer active")
		return
	}
	switch request.Action {
	case "dismiss":
		state.candidate = nil
		state.dismissed = true
		state.mu.Unlock()
		s.publishConferenceCandidate(nil)
		writeJSON(w, http.StatusOK, map[string]bool{"dismissed": true})
	case "exclude":
		if candidate.AppName == "" {
			state.mu.Unlock()
			writeError(w, http.StatusBadRequest, "cannot exclude an unnamed application")
			return
		}
		state.candidate = nil
		state.dismissed = true
		state.mu.Unlock()
		if err := s.rememberConferenceApp("conference_detection.excluded_apps", candidate.AppName); err != nil {
			s.logger.Printf("exclude app %q: %v", candidate.AppName, err)
		}
		s.publishConferenceCandidate(nil)
		writeJSON(w, http.StatusOK, map[string]any{"excluded": candidate.AppName})
	case "start", "trust":
		// "trust" — это "да, в доверенные и начать запись": приложение
		// запоминается и запись стартует сразу, без второго вопроса.
		if request.Action == "trust" && candidate.AppName == "" {
			state.mu.Unlock()
			writeError(w, http.StatusBadRequest, "cannot remember an unnamed application")
			return
		}
		state.candidate = nil
		state.dismissed = true
		state.mu.Unlock()
		if request.Action == "trust" {
			if err := s.rememberConferenceApp("conference_detection.trusted_apps", candidate.AppName); err != nil {
				s.logger.Printf("remember trusted app %q: %v", candidate.AppName, err)
			}
		}
		meeting, err := s.startMeetingRecording(r.Context(), model.RecordingStart{}, "detected", map[string]string{"conference_detection_backend": candidate.Backend})
		if err != nil {
			state.mu.Lock()
			state.candidate = candidate
			state.dismissed = false
			state.mu.Unlock()
			s.publishConferenceCandidate(candidate)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.publishConferenceCandidate(nil)
		writeJSON(w, http.StatusCreated, meeting)
	default:
		state.mu.Unlock()
		writeError(w, http.StatusBadRequest, "action must be start, dismiss, trust or exclude")
	}
}
