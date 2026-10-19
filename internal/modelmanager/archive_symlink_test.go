package modelmanager

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeLibraryArchive builds a tar.gz layout that matches the official
// llama.cpp release: the libraries are versioned regular files and the names
// the dynamic loader asks for are symlinks to them.
func writeLibraryArchive(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	writer := tar.NewWriter(gz)
	defer writer.Close()
	entries := []struct {
		name, link, content string
		symlink             bool
	}{
		{name: "llama-b11696/libllama.0.7.0.dylib", content: "llama"},
		{name: "llama-b11696/libllama.0.dylib", link: "libllama.0.7.0.dylib", symlink: true},
		{name: "llama-b11696/libggml.0.27.0.dylib", content: "ggml"},
		{name: "llama-b11696/libggml.0.dylib", link: "libggml.0.27.0.dylib", symlink: true},
		{name: "llama-b11696/libggml.dylib", link: "libggml.0.dylib", symlink: true},
		{name: "llama-b11696/llama-cli", content: "binary"},
		// A link to a file the filter drops must be skipped, not fatal.
		{name: "llama-b11696/libbad.dylib", link: "llama-cli", symlink: true},
		// An entry that would escape the output directory must be ignored.
		{name: "llama-b11696/../libescape.dylib", content: "escape"},
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o600}
		if entry.symlink {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.link
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.content))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if !entry.symlink {
			if _, err := writer.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestExtractArchiveMaterializesSymlinks guards the runtime installation: the
// archive stores libllama.0.dylib and libggml.0.dylib as symlinks, and the
// loader asks for exactly those names.
func TestExtractArchiveMaterializesSymlinks(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "llama.tar.gz")
	writeLibraryArchive(t, archive)
	output := filepath.Join(dir, "runtime")
	if err := extractArchiveFile(archive, output, "tgzlibs:llama-b11696/"); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := readFile(t, filepath.Join(output, "libllama.0.7.0.dylib")); got != "llama" {
		t.Fatalf("versioned library has wrong content: %q", got)
	}
	if got := readFile(t, filepath.Join(output, "libllama.0.dylib")); got != "llama" {
		t.Fatalf("loader name is missing or broken: %q", got)
	}
	// The chain libggml.dylib -> libggml.0.dylib -> libggml.0.27.0.dylib has to
	// resolve to the real file.
	if got := readFile(t, filepath.Join(output, "libggml.dylib")); got != "ggml" {
		t.Fatalf("chained loader name is missing or broken: %q", got)
	}
	if _, err := os.Stat(filepath.Join(output, "llama-cli")); !os.IsNotExist(err) {
		t.Fatal("a tool binary was installed as a library")
	}
	if _, err := os.Stat(filepath.Join(dir, "libescape.dylib")); !os.IsNotExist(err) {
		t.Fatal("an archive entry escaped the output directory")
	}
	// A broken link to a filtered file must not fail the installation and must
	// not leave a dangling entry behind.
	if _, err := os.Lstat(filepath.Join(output, "libbad.dylib")); !os.IsNotExist(err) {
		t.Fatal("a link to a filtered file was materialized")
	}
}

// TestExtractZipMaterializesSymlinks covers the zip layout used by the macOS
// whisper.cpp framework, where links are stored as entries with the target as
// content.
func TestExtractZipMaterializesSymlinks(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "libs.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	regular, err := writer.Create("lib/libllama.0.7.0.dylib")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := regular.Write([]byte("llama")); err != nil {
		t.Fatal(err)
	}
	header := &zip.FileHeader{Name: "lib/libllama.0.dylib", Method: zip.Deflate}
	header.SetMode(os.ModeSymlink | 0o777)
	link, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := link.Write([]byte("libllama.0.7.0.dylib")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "runtime")
	if err := extractArchiveFile(archive, output, "ziplibs:lib/"); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := readFile(t, filepath.Join(output, "libllama.0.dylib")); got != "llama" {
		t.Fatalf("zip link is missing or broken: %q", got)
	}
}
