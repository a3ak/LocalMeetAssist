// Package logging provides level filtering and log-file helpers.
package logging

import (
	"bytes"
	"io"
	"log"
	"strings"
	"sync/atomic"
)

// Level controls the minimum severity written by FilterWriter. Untagged
// messages are treated as info so older call sites remain useful.
type Level int32

const (
	Debug Level = iota
	Info
	Warn
	Error
)

// Controller holds the currently enabled log level and can change it at runtime.
type Controller struct{ level atomic.Int32 }

// NewController creates a controller for the given level name.
func NewController(value string) *Controller {
	c := &Controller{}
	c.Set(value)
	return c
}

// Parse maps a level name (debug/info/warn/error) to a Level.
func Parse(value string) Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return Debug
	case "warn", "warning":
		return Warn
	case "error":
		return Error
	default:
		return Info
	}
}

// Set changes the active log level at runtime.
func (c *Controller) Set(value string) { c.level.Store(int32(Parse(value))) }

// Enabled reports whether messages of the given level are logged.
func (c *Controller) Enabled(level Level) bool { return level >= Level(c.level.Load()) }

// FilterWriter drops records below the configured level before writing them.
type FilterWriter struct {
	Output     io.Writer
	Controller *Controller
}

// Write implements io.Writer and injects the level into the log record.
func (w FilterWriter) Write(p []byte) (int, error) {
	if w.Output == nil {
		return len(p), nil
	}
	level := Info
	text := string(p)
	for candidate, marker := range map[Level]string{Debug: "level=DEBUG", Info: "level=INFO", Warn: "level=WARN", Error: "level=ERROR"} {
		if strings.Contains(text, marker) {
			level = candidate
			break
		}
	}
	if w.Controller != nil && !w.Controller.Enabled(level) {
		return len(p), nil
	}
	if !strings.Contains(text, "level=") {
		p = bytes.Replace(p, []byte("localmeetassist "), []byte("localmeetassist level=INFO "), 1)
	}
	_, err := w.Output.Write(p)
	return len(p), err
}

// Debugf logs at debug level.
func Debugf(logger *log.Logger, format string, args ...any) {
	logger.Printf("level=DEBUG "+format, args...)
}

// Infof logs at info level.
func Infof(logger *log.Logger, format string, args ...any) {
	logger.Printf("level=INFO "+format, args...)
}

// Warnf logs at warning level.
func Warnf(logger *log.Logger, format string, args ...any) {
	logger.Printf("level=WARN "+format, args...)
}

// Errorf logs at error level.
func Errorf(logger *log.Logger, format string, args ...any) {
	logger.Printf("level=ERROR "+format, args...)
}
