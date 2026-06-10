package downloader

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownload_Smoke spins up a small in-process HTTP server that mimics a
// tiny website (index → about → contact, with an image and a PDF link) and
// crawls it depth=1. It checks the on-disk layout and the rewritten HTML.
func TestDownload_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-style test in -short mode")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body>
			<h1>Index</h1>
			<a href="/about.html">About</a>
			<a href="/handout.pdf">Handout</a>
			<img src="logo.png" alt="logo">
		</body></html>`))
	})
	mux.HandleFunc("/about.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body>
			<h1>About</h1>
			<a href="/">Home</a>
			<a href="#team">Team</a>
			<a href="https://other.example.com/x">External</a>
		</body></html>`))
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		// Minimal 1x1 PNG.
		png := []byte{
			0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
			'I', 'H', 'D', 'R', 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00,
			0x00, 0x0d, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00,
			0x00, 0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00,
			0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
		}
		w.Write(png)
	})
	mux.HandleFunc("/handout.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 fake"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	parsed, _ := url.Parse(srv.URL)
	hostDir := filepath.Join(tmp, strings.TrimPrefix(parsed.Hostname(), "www."))

	progressEvents := 0
	completeEvents := 0
	assetEvents := 0
	summary, err := Download(Options{
		URL:         srv.URL + "/",
		OutputDir:   hostDir,
		MaxDepth:    1,
		DownloadAll: true,
	}, Events{
		OnPage:     func(PageEvent) { progressEvents++ },
		OnAsset:    func(string, string) { assetEvents++ },
		OnComplete: func(Summary) { completeEvents++ },
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if summary.Pages < 1 {
		t.Errorf("expected ≥1 page, got %d", summary.Pages)
	}
	if progressEvents == 0 {
		t.Errorf("expected at least one OnPage event")
	}
	if assetEvents == 0 {
		t.Errorf("expected at least one OnAsset event")
	}
	if completeEvents != 1 {
		t.Errorf("expected exactly one OnComplete event, got %d", completeEvents)
	}

	// index.html must exist
	indexPath := filepath.Join(hostDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("expected %s to exist: %v", indexPath, err)
	}
	// logo.png must exist
	logoPath := filepath.Join(hostDir, "logo.png")
	if _, err := os.Stat(logoPath); err != nil {
		t.Fatalf("expected %s to exist: %v", logoPath, err)
	}
	// handout.pdf must exist (attachment)
	pdfPath := filepath.Join(hostDir, "handout.pdf")
	if _, err := os.Stat(pdfPath); err != nil {
		t.Fatalf("expected %s to exist: %v", pdfPath, err)
	}
	// The downloaded index.html should reference "logo.png" (relative).
	b, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `src="logo.png"`) {
		t.Errorf("expected rewritten src in index.html, got: %s", b)
	}
	// About page should have been crawled
	aboutPath := filepath.Join(hostDir, "about.html")
	if _, err := os.Stat(aboutPath); err != nil {
		t.Errorf("expected about.html to exist: %v", err)
	}
}
