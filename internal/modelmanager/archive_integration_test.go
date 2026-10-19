package modelmanager

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"localmeetassist/internal/llamacpp"
)

// TestInstallRealLlamaArchive installs a downloaded release archive with the
// same code path the model manager uses and checks that the expected library
// name is installed. On the host platform the native runtime is also loaded.
// It is skipped unless an archive is provided:
//
//	LM_LLAMA_ARCHIVE=/tmp/llama-b11696-bin-macos-arm64.tar.gz go test ./internal/modelmanager/ -run TestInstallRealLlamaArchive
//
// Optional: LM_LLAMA_ARCHIVE_DESCRIPTOR (default "tgzlibs:llama-b11696/") and
// LM_LLAMA_ARCHIVE_LIBRARY (default "libllama.0.dylib"). The Vulkan archives
// are verified the same way:
//
//	LM_LLAMA_ARCHIVE=/tmp/llama-b11696-bin-ubuntu-vulkan-x64.tar.gz \
//	LM_LLAMA_ARCHIVE_LIBRARY=libllama.so.0 go test ./internal/modelmanager/ -run TestInstallRealLlamaArchive
func TestInstallRealLlamaArchive(t *testing.T) {
	archive := os.Getenv("LM_LLAMA_ARCHIVE")
	if archive == "" {
		t.Skip("set LM_LLAMA_ARCHIVE to a downloaded llama.cpp release archive")
	}
	descriptor := os.Getenv("LM_LLAMA_ARCHIVE_DESCRIPTOR")
	if descriptor == "" {
		descriptor = "tgzlibs:llama-b11696/"
	}
	libraryName := os.Getenv("LM_LLAMA_ARCHIVE_LIBRARY")
	if libraryName == "" {
		libraryName = "libllama.0.dylib"
	}
	if strings.HasSuffix(strings.ToLower(archive), ".zip") {
		descriptor = "ziplibs:"
	}
	output := filepath.Join(t.TempDir(), "runtime")
	if err := extractArchiveFile(archive, output, descriptor); err != nil {
		t.Fatalf("extract: %v", err)
	}
	library := filepath.Join(output, libraryName)
	// The link may stay a link or become a copy where symlinks are not allowed;
	// either way it has to resolve to the real library.
	if !regularFile(library) {
		t.Fatalf("the loader name %s was not installed", libraryName)
	}
	// A library of another platform cannot be loaded here, so the extraction is
	// what such a run verifies.
	if !hostLibrary(libraryName) {
		t.Logf("%s belongs to another platform, loading skipped", libraryName)
		return
	}
	if err := llamacpp.Probe(library); err != nil {
		t.Fatalf("the installed runtime cannot be loaded: %v", err)
	}
}

// hostLibrary reports whether a library file name is loadable on this host.
func hostLibrary(name string) bool {
	lowered := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lowered, ".dylib"):
		return runtime.GOOS == "darwin"
	case strings.HasSuffix(lowered, ".dll"):
		return runtime.GOOS == "windows"
	default:
		return runtime.GOOS == "linux"
	}
}
