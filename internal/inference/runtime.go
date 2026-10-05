// Package inference wraps the ONNX Runtime shared library.
package inference

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var runtimeState struct {
	sync.Mutex
	path string
	err  error
}

// EnsureRuntime loads the native ONNX Runtime library exactly once. The path
// can point at a file downloaded by LocalMeetAssist or supplied manually while
// running the project with `go run`.
func EnsureRuntime(path string) error {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	if ort.IsInitialized() {
		if runtimeState.path != "" && filepath.Clean(runtimeState.path) != filepath.Clean(path) {
			return fmt.Errorf("ONNX Runtime is already loaded from %s; change the library after a restart", runtimeState.path)
		}
		return runtimeState.err
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return fmt.Errorf("ONNX Runtime library is unavailable at %s: %w", path, err)
	}
	ort.SetSharedLibraryPath(path)
	runtimeState.err = ort.InitializeEnvironment(ort.WithLogLevelWarning())
	if runtimeState.err != nil {
		return fmt.Errorf("load ONNX Runtime %s: %w", path, runtimeState.err)
	}
	runtimeState.path = path
	return nil
}

// ProbeModel opens an ONNX graph and validates the configured input/output
// names without running a potentially expensive inference.
func ProbeModel(path string, inputs, outputs []string) error {
	session, err := ort.NewDynamicAdvancedSession(path, inputs, outputs, nil)
	if err != nil {
		return err
	}
	return session.Destroy()
}
