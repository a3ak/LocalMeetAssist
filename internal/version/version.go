// Package version holds the single source of truth for the LocalMeetAssist version.
package version

// Version is reported by the CLI, the HTTP health/diagnostics endpoints and
// the startup log. It is a variable so release builds can inject the tag:
//
//	go build -ldflags "-X localmeetassist/internal/version.Version=0.1.0"
var Version = "0.1.0"
