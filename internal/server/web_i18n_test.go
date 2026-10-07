package server

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestWebTranslationsCoverUsedKeys guards the Web UI against raw i18n keys.
// t() falls back to the key itself, so a missing translation is invisible in
// code review and shows up only in the interface.
func TestWebTranslationsCoverUsedKeys(t *testing.T) {
	html := readWebFile(t, "web/index.html")
	script := readWebFile(t, "web/app.js")
	ru := readWebMessages(t, "web/lang/ru.json")
	en := readWebMessages(t, "web/lang/en.json")

	used := make(map[string]bool)
	// data-i18n, data-i18n-placeholder, data-i18n-title, data-i18n-aria-label.
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder|-title|-aria-label)?="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		used[m[1]] = true
	}
	// Literal t("key") calls. Keys composed from template literals are built at
	// runtime and are deliberately out of scope here.
	for _, m := range regexp.MustCompile(`(?:^|[^.\w])t\(\s*"([^"]+)"`).FindAllStringSubmatch(script, -1) {
		used[m[1]] = true
	}
	if len(used) < 100 {
		t.Fatalf("only %d keys extracted, the extractor probably broke", len(used))
	}

	for key := range used {
		if _, ok := ru[key]; !ok {
			t.Errorf("ru.json has no %q", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("en.json has no %q", key)
		}
	}
}

// TestWebTranslationsAreSymmetric keeps both languages in step: a key present in
// one file but not the other means one interface silently falls back.
func TestWebTranslationsAreSymmetric(t *testing.T) {
	ru := readWebMessages(t, "web/lang/ru.json")
	en := readWebMessages(t, "web/lang/en.json")
	for key := range ru {
		if _, ok := en[key]; !ok {
			t.Errorf("en.json is missing %q", key)
		}
	}
	for key := range en {
		if _, ok := ru[key]; !ok {
			t.Errorf("ru.json is missing %q", key)
		}
	}
}

func readWebFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readWebMessages(t *testing.T, path string) map[string]string {
	t.Helper()
	var messages map[string]string
	if err := json.Unmarshal([]byte(readWebFile(t, path)), &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}
