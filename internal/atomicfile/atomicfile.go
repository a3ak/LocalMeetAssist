// Package atomicfile provides atomic file writes shared by the pipeline and
// server packages. Files are written to a sibling temporary path and promoted
// with a rename, preserving the previous version as "<path>.previous" during
// the swap.
package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Write atomically writes data to path with the given permissions. On success
// the previous file (if any) is removed; on failure the previous file is
// restored.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".localmeetassist-candidate-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return Promote(tmpPath, path)
}

// Promote moves candidate over path, keeping a short-lived "<path>.previous"
// backup so a failed rename can be rolled back.
func Promote(candidate, path string) error {
	backup := path + ".previous"
	_ = os.Remove(backup)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(candidate, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

// Generate runs generate into a sibling candidate file and promotes it only
// when the result is a non-empty regular file.
func Generate(path string, generate func(string) error) error {
	ext := filepath.Ext(path)
	candidate := strings.TrimSuffix(path, ext) + ".candidate" + ext
	_ = os.Remove(candidate)
	if err := generate(candidate); err != nil {
		_ = os.Remove(candidate)
		return err
	}
	if info, err := os.Stat(candidate); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		_ = os.Remove(candidate)
		if err == nil {
			err = errors.New("candidate file is empty")
		}
		return err
	}
	return Promote(candidate, path)
}
