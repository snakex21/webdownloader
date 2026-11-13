package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadNonHTMLLinkIsFetchedOnce(t *testing.T) {
	var dataHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<a href="/export">data</a>`))
	})
	mux.HandleFunc("/export", func(w http.ResponseWriter, r *http.Request) {
		dataHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("binary-export"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 2}, Events{}); err != nil {
		t.Fatal(err)
	}
	if n := dataHits.Load(); n != 1 {
		t.Fatalf("non-HTML link fetched %d times, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(out, "export"))
	if err != nil || string(got) != "binary-export" {
		t.Fatalf("saved file = %q, %v", got, err)
	}
}

func TestDownloadDynamicImagesDoNotCollide(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<img src="/img.php?id=1"><img src="/img.php?id=2">`))
	})
	mux.HandleFunc("/img.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("image-" + r.URL.Query().Get("id")))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out}, Events{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		got, err := os.ReadFile(filepath.Join(out, "img__id_"+id+".php"))
		if err != nil || string(got) != "image-"+id {
			t.Errorf("image %s = %q, %v", id, got, err)
		}
	}
	html, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if !strings.Contains(string(html), "img__id_1.php") || !strings.Contains(string(html), "img__id_2.php") {
		t.Errorf("HTML not rewritten to distinct files: %s", html)
	}
}

func TestDownloadReportsFailedAssets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<img src="/missing.png"><a href="/big.zip">zip</a>`))
	})
	mux.HandleFunc("/big.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("z"), 4096))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	var uiErrors []string
	_, err := Download(Options{URL: srv.URL + "/", OutputDir: out, DownloadAll: true, MaxFileBytes: 1024, Retries: -1},
		Events{OnError: func(u string, err error) { uiErrors = append(uiErrors, u+" "+err.Error()) }})
	if err != nil {
		t.Fatal(err)
	}
	logData, _ := os.ReadFile(filepath.Join(out, "download-errors.log"))
	reportData, _ := os.ReadFile(filepath.Join(out, "download-report.json"))
	var report struct{ Errors []map[string]string }
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"missing.png", "big.zip"} {
		if !strings.Contains(string(logData), want) {
			t.Errorf("%s missing from download-errors.log:\n%s", want, logData)
		}
		if !strings.Contains(strings.Join(uiErrors, "\n"), want) {
			t.Errorf("%s not reported to the UI: %v", want, uiErrors)
		}
		found := false
		for _, e := range report.Errors {
			found = found || strings.Contains(e["url"], want)
		}
		if !found {
			t.Errorf("%s missing from download-report.json", want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "big.zip")); !os.IsNotExist(err) {
		t.Error("over-limit attachment saved")
	}
}

func TestDownloadCancelDuringAttachmentLeavesNoPartialFile(t *testing.T) {
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<a href="/video.mp4">video</a>`))
	})
	mux.HandleFunc("/video.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10000000")
		w.Write(bytes.Repeat([]byte("v"), 50000))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; time.Sleep(50 * time.Millisecond); cancel() }()
	out := t.TempDir()
	summary, err := Download(Options{Context: ctx, URL: srv.URL + "/", OutputDir: out, DownloadAll: true}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Cancelled {
		t.Error("summary not marked cancelled")
	}
	if _, err := os.Stat(filepath.Join(out, "video.mp4")); !os.IsNotExist(err) {
		t.Error("partial attachment left under its final name")
	}
	assertNoPartialFiles(t, out)
	logData, _ := os.ReadFile(filepath.Join(out, "download-errors.log"))
	if strings.Contains(string(logData), "video.mp4") {
		t.Errorf("user cancellation logged as an error: %s", logData)
	}
}
