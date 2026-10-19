package server

import (
	"regexp"
	"strings"
	"testing"
)

// TestHiddenClassBeatsImportantDisplay guards the CSS cascade of the settings
// form. Rows of the form set display with !important, so a plain .hidden rule
// (the one at the top of app.css) loses and a "hidden" field stays visible.
// That is exactly how the summary engine switch looked broken: the section
// heading disappeared while its fields remained.
func TestHiddenClassBeatsImportantDisplay(t *testing.T) {
	css := readWebFile(t, "web/app.css")
	start := strings.LastIndex(css, ".hidden.hidden")
	if start < 0 {
		t.Fatal("app.css has no .hidden.hidden override; hidden settings rows would stay visible")
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatal("the .hidden.hidden rule is not closed")
	}
	end += start
	if !strings.Contains(css[start:end], "display:none!important") {
		t.Fatal("the .hidden.hidden override does not collapse display")
	}
	// A later rule with !important display could win by order or specificity, so
	// the override has to come after every such rule.
	for _, match := range regexp.MustCompile(`display:[^;}]*!important`).FindAllStringIndex(css, -1) {
		if match[0] > end {
			t.Fatalf("an !important display rule after the .hidden override can win the cascade: %q", css[match[0]:match[1]])
		}
	}
}
