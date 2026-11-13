package downloader

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// partialSuffix marks in-progress files. They are written next to their final
// path and renamed into place only after the whole body was written, so an
// interrupted run (cancel, crash, disk full) never leaves a truncated file
// under the final name that the skip-existing logic would later trust.
const partialSuffix = ".wdpart"

// writeOutputFile confines both directory creation and file writes to the
// selected output tree, including when existing symlinks point outside it.
func writeOutputFile(baseDir, fullPath string, data []byte) error {
	_, err := writeOutputStream(baseDir, fullPath, bytes.NewReader(data))
	return err
}

// writeOutputStream streams r into fullPath (inside baseDir) atomically: the
// data goes to a temporary sibling file that is renamed over fullPath only on
// success and removed on any error. It returns the number of bytes written.
func writeOutputStream(baseDir, fullPath string, r io.Reader) (int64, error) {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	rel, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return 0, err
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return 0, err
	}
	// Never replace a link someone planted in the output tree; the rename
	// below would not follow it, but silently swapping it is surprising.
	if info, err := root.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("refusing to overwrite symlink %q", rel)
	}

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	tmp := rel + "." + hex.EncodeToString(suffix[:]) + partialSuffix
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = root.Remove(tmp)
		return n, err
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return n, err
	}
	return n, nil
}

func readOutputFile(baseDir, fullPath string) ([]byte, error) {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return nil, err
	}
	return root.ReadFile(rel)
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

// removeOutputFile deletes a file inside baseDir (confined like writes).
func removeOutputFile(baseDir, fullPath string) error {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return err
	}
	return root.Remove(rel)
}
