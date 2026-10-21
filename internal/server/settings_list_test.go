package server

import (
	"strings"
	"testing"

	"localmeetassist/internal/config"
)

// TestConferenceAppListsAreEditableSettings guards the manual editors for the
// trusted and excluded application lists: the schema must expose them as list
// fields, and the comma-separated value must round-trip into a TOML array.
func TestConferenceAppListsAreEditableSettings(t *testing.T) {
	cfg := config.Defaults()
	cfg.ConferenceDetection.TrustedApps = []string{"Zoom Workplace", "Microsoft Teams"}
	cfg.ConferenceDetection.ExcludedApps = []string{"Voice Memos"}

	fields := map[string]settingField{}
	for _, group := range settingsSchema(cfg, nil) {
		for _, field := range group.Fields {
			fields[field.Key] = field
		}
	}

	trusted, ok := fields["conference_detection.trusted_apps"]
	if !ok {
		t.Fatal("trusted apps field is missing from the settings schema")
	}
	if trusted.Kind != "apps" {
		t.Fatalf("trusted apps kind = %q, want apps", trusted.Kind)
	}
	if len(trusted.Items) != 2 || trusted.Items[0].Real != "Zoom Workplace" || trusted.Items[0].Visible != "Zoom Workplace" {
		t.Fatalf("trusted apps items = %+v", trusted.Items)
	}
	if !strings.Contains(trusted.Value, "Zoom Workplace = Zoom Workplace") {
		t.Fatalf("trusted apps value = %q", trusted.Value)
	}
	excluded, ok := fields["conference_detection.excluded_apps"]
	if !ok || excluded.Kind != "list" || excluded.Value != "Voice Memos" {
		t.Fatalf("excluded apps field mismatch: %+v", excluded)
	}

	parsed := splitSettingList(" Zoom Workplace , , Microsoft Teams ")
	if len(parsed) != 2 || parsed[0] != "Zoom Workplace" || parsed[1] != "Microsoft Teams" {
		t.Fatalf("splitSettingList = %v", parsed)
	}
	if got := config.QuotedList(parsed); got != `["Zoom Workplace", "Microsoft Teams"]` {
		t.Fatalf("QuotedList = %s", got)
	}
	if list := splitSettingList(""); len(list) != 0 {
		t.Fatalf("empty input must produce an empty list, got %v", list)
	}
}
