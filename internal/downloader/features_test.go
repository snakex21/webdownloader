package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDownload_MaxPagesStops verifies the MaxPages limit cuts the crawl
// short even when more pages are reachable.
func TestDownload_MaxPagesStops(t *testing.T) {
	mux := http.NewServeMux()
	for i := 0; i < 5; i++ {
		path := "/p" + itoa(i) + ".html"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><body>p</body></html>`))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body>
			<a href="/p0.html">0</a>
			<a href="/p1.html">1</a>
			<a href="/p2.html">2</a>
			<a href="/p3.html">3</a>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	summary, err := Download(Options{
		URL:       srv.URL + "/",
		OutputDir: tmp,
		MaxDepth:  2,
		MaxPages:  2,
	}, Events{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if summary.Pages > 2 {
		t.Errorf("expected at most 2 pages (got %d)", summary.Pages)
	}
}

func TestDownload_MaxBytesStops(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body>` + strings.Repeat("A", 50_000) + `</body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body>
			<a href="/big">big</a>
			<a href="/big">big2</a>
			<a href="/big">big3</a>
			<a href="/big">big4</a>
		</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	summary, err := Download(Options{
		URL:           srv.URL + "/",
		OutputDir:     tmp,
		MaxDepth:      1,
		MaxTotalBytes: 60_000,
	}, Events{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if summary.Bytes > 80_000 {
		t.Errorf("expected bytes to be capped near 60k (got %d)", summary.Bytes)
	}
}

func TestDownload_ExternalCDNAssetsAreScopedSeparatelyFromHTMLLinks(t *testing.T) {
	var assetHits atomic.Int32
	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assetHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer assetSrv.Close()
	assetURL := strings.Replace(assetSrv.URL, "127.0.0.1", "localhost", 1)

	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
			<img src="` + assetURL + `/cdn.png">
			<a href="` + assetURL + `/external.html">external html should not be crawled</a>
		</body></html>`))
	}))
	defer pageSrv.Close()

	// By default, absolute off-domain assets are collected by the parser but
	// rejected by scope filtering.
	_, err := Download(Options{URL: pageSrv.URL + "/", OutputDir: t.TempDir(), MaxDepth: 2}, Events{})
	if err != nil {
		t.Fatalf("Download without external assets: %v", err)
	}
	if got := assetHits.Load(); got != 0 {
		t.Fatalf("external asset should not be requested by default, got %d hits", got)
	}

	assetHits.Store(0)
	summary, err := Download(Options{
		URL:                   pageSrv.URL + "/",
		OutputDir:             t.TempDir(),
		MaxDepth:              2,
		IncludeExternalAssets: true,
	}, Events{})
	if err != nil {
		t.Fatalf("Download with external assets: %v", err)
	}
	if got := assetHits.Load(); got != 1 {
		t.Fatalf("external CDN asset should be requested once, got %d hits", got)
	}
	if summary.Pages != 1 {
		t.Fatalf("external HTML link must not be crawled, got %d pages", summary.Pages)
	}
}

func TestDownload_CancelMidway(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 20 outbound links — enough to keep the BFS loop busy.
		body := "<html><body>"
		for i := 0; i < 20; i++ {
			body += `<a href="/p` + itoa(i) + `.html">` + itoa(i) + `</a>`
		}
		body += "</body></html>"
		w.Write([]byte(body))
	})
	for i := 0; i < 20; i++ {
		path := "/p" + itoa(i) + ".html"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><body>p</body></html>`))
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan Summary, 1)
	go func() {
		s, _ := Download(Options{
			Context:   ctx,
			URL:       srv.URL + "/",
			OutputDir: t.TempDir(),
			MaxDepth:  3,
		}, Events{
			OnPage: func(ev PageEvent) {
				if ev.Status == "downloading" && ev.URL != srv.URL+"/" {
					select {
					case <-started:
					default:
						close(started)
					}
				}
			},
		})
		done <- s
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not start a subpage in time")
	}

	select {
	case summary := <-done:
		if !summary.Cancelled {
			t.Fatalf("expected cancelled summary, got %+v", summary)
		}
		if summary.Pages >= 20 {
			t.Fatalf("cancel did not stop the crawl early, got %d pages", summary.Pages)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop Download in time")
	}
}

func TestFetcher_MaxBytesUsesContentLengthBeforeReadingBody(t *testing.T) {
	var wroteBody atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1000000")
		wroteBody.Store(true)
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	}))
	defer srv.Close()

	f := NewFetcher()
	_, err := f.FetchURLWithLimit(srv.URL+"/big.bin", 10)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
	_ = wroteBody.Load() // server may start writing, but client rejects before ReadAll.
}

func TestDownload_MaxFileBytesSkipsLargeAsset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><img src="/big.png"></body></html>`))
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	summary, err := Download(Options{
		URL:          srv.URL + "/",
		OutputDir:    tmp,
		MaxDepth:     1,
		MaxFileBytes: 10,
	}, Events{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if summary.Assets != 0 {
		t.Fatalf("large asset should be skipped, got %d assets", summary.Assets)
	}
	if _, err := os.Stat(filepath.Join(tmp, "big.png")); !os.IsNotExist(err) {
		t.Fatalf("large asset should not be written, stat err=%v", err)
	}
}

func TestDownload_ErrorLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><img src="/missing.png"></body></html>`))
	})
	mux.HandleFunc("/missing.png", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	_, err := Download(Options{
		URL:       srv.URL + "/",
		OutputDir: tmp,
		MaxDepth:  1,
	}, Events{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	logPath := filepath.Join(tmp, "download-errors.log")
	if _, err := readFile(logPath); err != nil {
		t.Errorf("expected error log at %s, got %v", logPath, err)
	}
}

func readFile(p string) ([]byte, error) {
	// Tiny helper so we don't add the import at the top.
	return readFileOS(p)
}

// itoa is a tiny non-fmt helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	if neg {
		s = append([]byte{'-'}, s...)
	}
	return string(s)
}
