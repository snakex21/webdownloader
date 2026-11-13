package downloader

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingReader returns some data and then an error, simulating a dropped
// connection, a cancelled context or a full disk in the middle of a file.
type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// assertNoPartialFiles fails if any in-progress temp file is left in dir.
func assertNoPartialFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, partialSuffix) {
			t.Errorf("partial file left behind: %s", path)
		}
		return nil
	})
}

func TestWriteOutputStreamFailureLeavesNoFile(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(out, "sub", "file.bin")
	errDisk := errors.New("simulated: there is not enough space on the disk")

	_, err := writeOutputStream(out, target, &failingReader{data: []byte("half"), err: errDisk})
	if !errors.Is(err, errDisk) {
		t.Fatalf("err = %v, want %v", err, errDisk)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("final file exists after failed write: %v", statErr)
	}
	assertNoPartialFiles(t, out)
}

func TestWriteOutputStreamFailureKeepsPreviousFile(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(out, "file.bin")
	if err := os.WriteFile(target, []byte("previous complete copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := writeOutputStream(out, target, &failingReader{data: []byte("new"), err: io.ErrUnexpectedEOF})
	if err == nil {
		t.Fatal("expected error")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "previous complete copy" {
		t.Fatalf("previous file clobbered: %q", got)
	}
	assertNoPartialFiles(t, out)
}

func TestWriteOutputStreamReplacesAtomically(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(out, "a", "b.txt")
	for _, body := range []string{"first", "second"} {
		n, err := writeOutputStream(out, target, strings.NewReader(body))
		if err != nil || n != int64(len(body)) {
			t.Fatalf("write %q: n=%d err=%v", body, n, err)
		}
	}
	got, _ := os.ReadFile(target)
	if string(got) != "second" {
		t.Fatalf("got %q", got)
	}
	assertNoPartialFiles(t, out)
}

func TestWriteOutputFileReportsPermissionError(t *testing.T) {
	out := t.TempDir()
	// A directory occupying the target name makes the final rename fail on
	// every OS, standing in for "access denied" on a read-only/locked file.
	target := filepath.Join(out, "taken")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputFile(out, target, []byte("x")); err == nil {
		t.Fatal("expected an error writing over a non-empty directory")
	}
	assertNoPartialFiles(t, out)
}
