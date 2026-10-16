package modelmanager

import "testing"

// TestLibraryNameWanted guards the runtime extraction filter: llama.cpp ships
// llama.dll together with ggml*.dll, libomp.dll and mtmd.dll, so a filter that
// only knows whisper.cpp would unpack almost nothing and the runtime would fail
// to load.
func TestLibraryNameWanted(t *testing.T) {
	wanted := []string{
		"llama.dll", "ggml.dll", "ggml-base.dll", "ggml-cpu.dll", "libomp.dll", "mtmd.dll",
		"libllama.0.dylib", "libllama.0.7.0.dylib", "libllama.so.0",
		"libwhisper.so.1.9.4", "whisper.dll",
	}
	for _, name := range wanted {
		if !libraryNameWanted(name) {
			t.Fatalf("%s должен считаться библиотекой", name)
		}
	}
	skipped := []string{
		"llama-cli", "llama-server.exe", "llama-bench.exe", "llama-quantize",
		"README.md", "libcrypto.so.3", "some.dll", "",
	}
	for _, name := range skipped {
		if libraryNameWanted(name) {
			t.Fatalf("%s не должен считаться библиотекой", name)
		}
	}
}
