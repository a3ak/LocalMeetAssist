package logging

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestFilterWriterLevelsAndLabels(t *testing.T) {
	var output bytes.Buffer
	controller := NewController("info")
	logger := log.New(FilterWriter{Output: &output, Controller: controller}, "localmeetassist ", 0)
	Debugf(logger, "poll")
	logger.Print("started")
	Warnf(logger, "slow")
	text := output.String()
	if strings.Contains(text, "poll") {
		t.Fatal("debug message was not filtered")
	}
	if !strings.Contains(text, "level=INFO started") || !strings.Contains(text, "level=WARN slow") {
		t.Fatalf("missing severity labels: %q", text)
	}
	controller.Set("error")
	Warnf(logger, "hidden")
	Errorf(logger, "visible")
	if strings.Contains(output.String(), "hidden") || !strings.Contains(output.String(), "level=ERROR visible") {
		t.Fatalf("runtime level was not applied: %q", output.String())
	}
}
