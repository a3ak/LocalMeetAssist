package uuidv7

import (
	"regexp"
	"testing"
)

func TestNewShapeAndVersion(t *testing.T) {
	v := New()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(v) {
		t.Fatalf("unexpected UUIDv7: %s", v)
	}
}
