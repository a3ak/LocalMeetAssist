package server

import (
	"testing"

	"localmeetassist/internal/config"
)

func hasCyrillic(value string) bool {
	for _, r := range value {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// TestSettingsSchemaIsFullyLocalizedWhenEnglish fails when any group, field,
// option or section still carries Russian text in the English schema. It is the
// guard that keeps settings_i18n.go in sync with settingsSchema.
func TestSettingsSchemaIsFullyLocalizedWhenEnglish(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.Language = "en"
	for _, group := range settingsSchema(cfg) {
		if hasCyrillic(group.Title) {
			t.Fatalf("group %s title is not localized: %q", group.ID, group.Title)
		}
		if hasCyrillic(group.Description) {
			t.Fatalf("group %s description is not localized: %q", group.ID, group.Description)
		}
		for _, field := range group.Fields {
			if hasCyrillic(field.Label) {
				t.Fatalf("field %s label is not localized: %q", field.Key, field.Label)
			}
			if hasCyrillic(field.Description) {
				t.Fatalf("field %s description is not localized: %q", field.Key, field.Description)
			}
			if hasCyrillic(field.Section) {
				t.Fatalf("field %s section is not localized: %q", field.Key, field.Section)
			}
			for _, option := range field.Options {
				if hasCyrillic(option.Label) {
					t.Fatalf("field %s option %q is not localized: %q", field.Key, option.Value, option.Label)
				}
			}
		}
	}
}

// TestSettingsSchemaKeepsRussianByDefault checks that the inline Russian text
// remains the source of truth when the interface language is Russian.
func TestSettingsSchemaKeepsRussianByDefault(t *testing.T) {
	cfg := config.Defaults()
	groups := settingsSchema(cfg)
	if len(groups) == 0 || groups[0].Title != "Приложение" {
		t.Fatalf("expected Russian schema by default, got %q", groups[0].Title)
	}
}

// TestSettingsSchemaKeepsKeysAcrossLanguages makes sure localization only
// changes presentation and never the keys used for validation and saving.
func TestSettingsSchemaKeepsKeysAcrossLanguages(t *testing.T) {
	ru := config.Defaults()
	en := config.Defaults()
	en.App.Language = "en"

	keys := func(cfg config.Config) []string {
		var out []string
		for _, group := range settingsSchema(cfg) {
			for _, field := range group.Fields {
				out = append(out, field.Key)
			}
		}
		return out
	}

	ruKeys, enKeys := keys(ru), keys(en)
	if len(ruKeys) != len(enKeys) {
		t.Fatalf("field count differs: ru=%d en=%d", len(ruKeys), len(enKeys))
	}
	for i := range ruKeys {
		if ruKeys[i] != enKeys[i] {
			t.Fatalf("field key mismatch at %d: ru=%q en=%q", i, ruKeys[i], enKeys[i])
		}
	}
}
