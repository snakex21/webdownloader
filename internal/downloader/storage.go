package downloader

import (
	"os"
	"path/filepath"
)

// writeOutputFile confines both directory creation and file writes to the
// selected output tree, including when existing symlinks point outside it.
func writeOutputFile(baseDir, fullPath string, data []byte) error {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return err
	}
	return root.WriteFile(rel, data, 0o644)
}

func statOutputFile(baseDir, fullPath string) (os.FileInfo, error) {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return nil, err
	}
	return root.Stat(rel)
}
