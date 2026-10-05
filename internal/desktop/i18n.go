package desktop

import "strings"

// english reports whether the configured interface language selects English.
func english(language string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en")
}

// tr returns the English variant when the configured language is English. Tray
// labels are built once at startup, so a language change needs a restart.
func (m *Manager) tr(ru, en string) string {
	if m.config != nil && english(m.config().App.Language) {
		return en
	}
	return ru
}
