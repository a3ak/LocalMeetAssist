package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePathPrefersTheWorkingDirectory keeps `go run` and the portable
// archive working: config.toml next to the binary wins over the user directory.
func TestResolvePathPrefersTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[app]\nlanguage = \"ru\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != "config.toml" {
		t.Fatalf("ResolvePath = %q, want config.toml", got)
	}
	if got := ResolvePath("./custom.toml"); got != "./custom.toml" {
		t.Fatalf("an explicit path must win, got %q", got)
	}
}

// TestResolvePathFallsBackToTheUserDirectory covers the Finder launch of a
// macOS bundle: the working directory is "/" and there is no config.toml there.
func TestResolvePathFallsBackToTheUserDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	want := DefaultPath()
	if want == "config.toml" {
		t.Skip("the platform has no user configuration directory")
	}
	if got := ResolvePath(""); got != want {
		t.Fatalf("ResolvePath = %q, want %q", got, want)
	}
	if !filepath.IsAbs(want) {
		t.Fatalf("the user configuration path must be absolute: %q", want)
	}
	if filepath.Base(filepath.Dir(want)) != "LocalMeetAssist" || filepath.Base(want) != "config.toml" {
		t.Fatalf("unexpected user configuration path: %q", want)
	}
}
