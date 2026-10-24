package store

import (
	"io"
	"os"
	"path/filepath"
)

// SaveUpload writes an uploaded file into DataDir/uploads and returns
// the file name. Callers have already validated the name and content.
func SaveUpload(dataDir, name string, r io.Reader) (string, error) {
	dir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return name, nil
}
