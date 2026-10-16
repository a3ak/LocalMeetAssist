//go:build cgo && linux

package nativeaudio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func linuxAudioActivity(backend string) Activity {
	if _, err := exec.LookPath("pactl"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		micText, micErr := pactlOutput(ctx, "source-outputs")
		sysText, sysErr := pactlOutput(ctx, "sink-inputs")
		if micErr == nil && sysErr == nil {
			return Activity{
				Microphone:    containsRunning(micText),
				System:        containsRunning(sysText),
				Available:     true,
				Backend:       backend + " activity via pactl",
				MicrophoneApp: runningSourceAppName(micText),
			}
		}
	}
	microphone := alsaPCMRunning("c")
	system := alsaPCMRunning("p")
	if microphone.known || system.known {
		return Activity{Microphone: microphone.running, System: system.running, Available: microphone.known && system.known, Backend: backend + " activity via ALSA", Warning: "PulseAudio/PipeWire session query unavailable; ALSA fallback may be less precise"}
	}
	return Activity{Backend: backend, Warning: "conference detection needs pactl (PulseAudio/PipeWire) or visible ALSA PCM status"}
}

func pactlOutput(ctx context.Context, kind string) (string, error) {
	output, err := exec.CommandContext(ctx, "pactl", "list", kind).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func containsRunning(text string) bool {
	return strings.Contains(strings.ToUpper(text), "STATE: RUNNING")
}

// runningSourceAppName extracts the application name of the first RUNNING
// source-output — the process actively recording the microphone.
func runningSourceAppName(text string) string {
	for _, block := range splitPactlBlocks(text, "Source Output") {
		if !containsRunning(block) {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "application.name = ") {
				return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "application.name = ")), "\"")
			}
		}
	}
	return ""
}

func splitPactlBlocks(text, header string) []string {
	var blocks []string
	var current strings.Builder
	started := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), header+" #") {
			if started {
				blocks = append(blocks, current.String())
			}
			current.Reset()
			current.WriteString(line)
			current.WriteString("\n")
			started = true
		} else if started {
			current.WriteString(line)
			current.WriteString("\n")
		}
	}
	if started {
		blocks = append(blocks, current.String())
	}
	return blocks
}

type alsaActivity struct{ known, running bool }

func alsaPCMRunning(direction string) alsaActivity {
	paths, _ := filepath.Glob("/proc/asound/card*/pcm*" + direction + "/sub*/status")
	result := alsaActivity{known: len(paths) > 0}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(strings.ToUpper(string(data)), "STATE: RUNNING") {
			result.running = true
			break
		}
	}
	return result
}
