package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/model"
)

// searchClause is one parsed search condition.
type searchClause struct {
	field    string
	values   []string
	negative []string
	after    *time.Time
	before   *time.Time
}

// normalizeSearch lowercases and collapses whitespace for matching.
func normalizeSearch(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

// parseSearchDate parses a DD.MM.YYYY date, optionally to the end of the day.
func parseSearchDate(value string, endOfDay bool) (*time.Time, error) {
	parsed, err := time.ParseInLocation("02.01.2006", strings.TrimSpace(value), time.Local)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q, expected DD.MM.YYYY", value)
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return &parsed, nil
}

// parseMeetingSearch splits a semicolon-separated query into clauses.
func parseMeetingSearch(query string) ([]searchClause, error) {
	var clauses []searchClause
	for _, raw := range strings.Split(query, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		field, value := "all", raw
		if before, after, ok := strings.Cut(raw, ":"); ok {
			field, value = normalizeSearch(before), strings.TrimSpace(after)
			switch field {
			case "speakers", "meeting", "discussion", "transcript", "summary", "date":
			default:
				return nil, fmt.Errorf("unknown search key %q", strings.TrimSpace(before))
			}
		}
		clause := searchClause{field: field}
		if field == "date" {
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				lower := strings.ToLower(part)
				switch {
				case strings.HasPrefix(lower, "after "):
					date, err := parseSearchDate(strings.TrimSpace(part[len("after "):]), false)
					if err != nil {
						return nil, err
					}
					clause.after = date
				case strings.HasPrefix(lower, "before "):
					date, err := parseSearchDate(strings.TrimSpace(part[len("before "):]), true)
					if err != nil {
						return nil, err
					}
					clause.before = date
				default:
					return nil, fmt.Errorf("for date use \"after DD.MM.YYYY\" and/or \"before DD.MM.YYYY\"")
				}
			}
		} else if field == "speakers" {
			for _, part := range strings.Split(value, ",") {
				part = normalizeSearch(part)
				if part == "" {
					continue
				}
				if strings.HasPrefix(part, "!") {
					if excluded := normalizeSearch(strings.TrimPrefix(part, "!")); excluded != "" {
						clause.negative = append(clause.negative, excluded)
					}
				} else {
					clause.values = append(clause.values, part)
				}
			}
		} else if value = normalizeSearch(value); value != "" {
			clause.values = []string{value}
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

// containsPhrase reports whether the normalized haystack contains the phrase.
func containsPhrase(haystack, phrase string) bool {
	return strings.Contains(normalizeSearch(haystack), phrase)
}

// matchesMeetingSearch applies all clauses to one meeting.
func matchesMeetingSearch(meeting model.Meeting, speakers []model.Speaker, clauses []searchClause) bool {
	names := make([]string, 0, len(speakers))
	for _, speaker := range speakers {
		name := speaker.DisplayName
		if strings.TrimSpace(name) == "" {
			name = speaker.ID
		}
		names = append(names, normalizeSearch(name))
	}
	speakerText := strings.Join(names, "\n")
	for _, clause := range clauses {
		switch clause.field {
		case "date":
			started := meeting.StartedAt.In(time.Local)
			if clause.after != nil && started.Before(*clause.after) {
				return false
			}
			if clause.before != nil && started.After(*clause.before) {
				return false
			}
		case "speakers":
			for _, value := range clause.values {
				if !containsPhrase(speakerText, value) {
					return false
				}
			}
			for _, value := range clause.negative {
				if containsPhrase(speakerText, value) {
					return false
				}
			}
		default:
			var corpus string
			switch clause.field {
			case "meeting":
				corpus = meeting.Title
			case "discussion":
				corpus = meeting.Transcript + "\n" + meeting.Summary
			case "transcript":
				corpus = meeting.Transcript
			case "summary":
				corpus = meeting.Summary
			default:
				corpus = meeting.Title + "\n" + speakerText + "\n" + meeting.Transcript + "\n" + meeting.Summary
			}
			for _, phrase := range clause.values {
				if !containsPhrase(corpus, phrase) {
					return false
				}
			}
		}
	}
	return true
}

// searchMeetings returns the UIDs of meetings matching a structured query.
func (s *Server) searchMeetings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	clauses, err := parseMeetingSearch(r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	matching := make([]string, 0, len(meetings))
	knownSpeakers := make(map[string]struct{})
	for _, meeting := range meetings {
		speakers, _ := s.store.Speakers(meeting.UID)
		for _, speaker := range speakers {
			name := strings.TrimSpace(speaker.DisplayName)
			if name != "" {
				knownSpeakers[name] = struct{}{}
			}
		}
		if matchesMeetingSearch(meeting, speakers, clauses) {
			matching = append(matching, meeting.UID)
		}
	}
	names := make([]string, 0, len(knownSpeakers))
	for name := range knownSpeakers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	writeJSON(w, http.StatusOK, map[string]any{"matching_uids": matching, "speaker_names": names})
}
