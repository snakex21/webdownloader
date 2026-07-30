package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDownloadRequest(t *testing.T) {
	request, err := parseDownloadRequest(`{"url":"https://example.com","depth":3}`)
	if err != nil {
		t.Fatalf("parseDownloadRequest() error = %v", err)
	}
	if request.URL != "https://example.com" || request.Depth != 3 {
		t.Fatalf("parseDownloadRequest() = %#v", request)
	}
}

func TestValidateDownloadURL(t *testing.T) {
	for _, rawURL := range []string{"", "example.com", "://broken"} {
		if _, err := validateDownloadURL(rawURL); err == nil {
			t.Errorf("validateDownloadURL(%q) expected an error", rawURL)
		}
	}

	if _, err := validateDownloadURL("https://example.com/path"); err != nil {
		t.Fatalf("validateDownloadURL() error = %v", err)
	}
}

func TestNextAvailableOutputDir(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(baseDir, "example.com_v2"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := nextAvailableOutputDir(baseDir, "example.com")
	want := filepath.Join(baseDir, "example.com_v3")
	if got != want {
		t.Fatalf("nextAvailableOutputDir() = %q, want %q", got, want)
	}
}

func TestMegabytes(t *testing.T) {
	if got, want := megabytes(25), int64(25*1024*1024); got != want {
		t.Fatalf("megabytes(25) = %d, want %d", got, want)
	}
}
