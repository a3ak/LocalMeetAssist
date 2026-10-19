package server

import (
	"slices"
	"testing"

	"localmeetassist/internal/config"
)

// summarySections returns the summary group of the schema.
func summarySections(t *testing.T) settingGroup {
	t.Helper()
	for _, group := range settingsSchema(config.Defaults()) {
		if group.ID == "summary" {
			return group
		}
	}
	t.Fatal("the summary settings group is missing")
	return settingGroup{}
}

// TestSummarySettingsShowOnlyTheSelectedEngine guards the split of the summary
// settings: general settings are always visible, while the network endpoint and
// the local model settings are shown only for the engine that uses them.
func TestSummarySettingsShowOnlyTheSelectedEngine(t *testing.T) {
	group := summarySections(t)

	// Every field must stay in the schema: hiding is a display concern, and a
	// dropped field would also disappear from the saved values.
	wantKeys := []string{
		"summary.auto_run", "summary.engine", "summary.language", "summary.system_prompt",
		"summary.base_url", "summary.model", "summary.token", "summary.token_env",
		"summary.timeout_seconds", "summary.max_retries", "summary.tls_verify",
		"summary.tls_ca_file", "summary.tls_server_name",
		"summary.model_path", "summary.context_tokens", "summary.max_tokens",
		"summary.threads", "summary.gpu_layers", "summary.temperature",
		"summary.local_timeout_seconds",
	}
	seen := make(map[string]bool, len(group.Fields))
	sections := make([]string, 0, 3)
	for _, field := range group.Fields {
		seen[field.Key] = true
		if len(sections) == 0 || sections[len(sections)-1] != field.Section {
			sections = append(sections, field.Section)
		}
	}
	for _, key := range wantKeys {
		if !seen[key] {
			t.Fatalf("field %s disappeared from the summary schema", key)
		}
	}
	if len(seen) != len(wantKeys) {
		t.Fatalf("unexpected field count: %d instead of %d", len(seen), len(wantKeys))
	}
	// A heading is printed when the section changes, so each section has to be
	// one contiguous run of fields.
	if want := []string{"Общие параметры", "Сетевой эндпоинт", "Локальная модель"}; !slices.Equal(sections, want) {
		t.Fatalf("summary sections are %q, want %q", sections, want)
	}

	for _, field := range group.Fields {
		switch field.Section {
		case "Общие параметры":
			if field.ShowIf != nil {
				t.Fatalf("general field %s is hidden by a condition", field.Key)
			}
		case "Сетевой эндпоинт":
			if field.ShowIf == nil || field.ShowIf.Key != "summary.engine" {
				t.Fatalf("network field %s has no engine condition: %+v", field.Key, field.ShowIf)
			}
			if !slices.Equal(field.ShowIf.Values, []string{config.EngineOpenAI}) {
				t.Fatalf("network field %s is shown for %v", field.Key, field.ShowIf.Values)
			}
		case "Локальная модель":
			if field.ShowIf == nil || field.ShowIf.Key != "summary.engine" {
				t.Fatalf("local field %s has no engine condition: %+v", field.Key, field.ShowIf)
			}
			if !slices.Equal(field.ShowIf.Values, []string{config.EngineLocalLlama}) {
				t.Fatalf("local field %s is shown for %v", field.Key, field.ShowIf.Values)
			}
		default:
			t.Fatalf("field %s has an unexpected section %q", field.Key, field.Section)
		}
	}
}

// TestSummarySettingsConditionTargetsAnExistingField keeps the visibility rule
// resolvable by the interface: the controlling select has to be in the same
// group, otherwise nothing would ever toggle the sections.
func TestSummarySettingsConditionTargetsAnExistingField(t *testing.T) {
	group := summarySections(t)
	keys := make(map[string]bool, len(group.Fields))
	for _, field := range group.Fields {
		keys[field.Key] = true
	}
	for _, field := range group.Fields {
		if field.ShowIf != nil && !keys[field.ShowIf.Key] {
			t.Fatalf("field %s depends on %s, which is not in the group", field.Key, field.ShowIf.Key)
		}
	}
}
