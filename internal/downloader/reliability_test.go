package downloader

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOutputPathsRejectUnsafeNames(t *testing.T) {
	for _, path := range []string{"/../escape.txt", "/%2e%2e/escape.txt", "/a/%2e%2e/escape.txt", "/%5c..%5cescape.txt", "/C:%5cescape.txt", "/NUL.txt", "/foo./bar", "/a%00b"} {
		for name, mapper := range map[string]func(string, string) (string, error){"page": FilePathFor, "asset": AssetPathFor} {
			t.Run(name+path, func(t *testing.T) {
				if got, err := mapper(t.TempDir(), "https://example.com"+path); err == nil {
					t.Fatalf("unsafe path accepted: %s", got)
				}
			})
		}
	}
}

func TestDownloadRejectsInvalidURLBeforeCreatingOutput(t *testing.T) {
	for _, raw := range []string{"", "example.com", "file:///tmp/test", "https:///missing-host"} {
		out := filepath.Join(t.TempDir(), "not-created")
		_, err := Download(Options{URL: raw, OutputDir: out}, Events{})
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%q: expected ErrInvalidURL, got %v", raw, err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("%q created output", raw)
		}
	}
}

func TestDownloadRewritesSharedAssetsOnEveryPage(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img/logo.png" {
			hits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("image"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<img src="/img/logo.png"><img src="/img/logo.png"><a href="/nested/page.html">next</a>`))
	}))
	defer srv.Close()
	out := t.TempDir()
	summary, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 1}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(out, "nested", "page.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `src="/img/logo.png"`) {
		t.Fatal("shared asset still points to server-root URL")
	}
	if strings.Count(string(body), `src="../img/logo.png"`) != 2 {
		t.Fatalf("bad shared references: %s", body)
	}
	if hits.Load() != 1 || summary.Assets != 1 {
		t.Fatalf("hits=%d assets=%d; want one download", hits.Load(), summary.Assets)
	}
}

func TestDownloadMaxFileBytesAppliesToHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(strings.Repeat("x", 1024)))
	}))
	defer srv.Close()
	out := t.TempDir()
	var sizeError bool
	s, err := Download(Options{URL: srv.URL, OutputDir: out, MaxFileBytes: 64}, Events{OnError: func(_ string, err error) { sizeError = errors.Is(err, ErrFileTooLarge) }})
	if err != nil {
		t.Fatal(err)
	}
	if s.Pages != 0 || !sizeError {
		t.Fatalf("oversized HTML counted as saved: %+v, size error=%v", s, sizeError)
	}
	if _, err := os.Stat(filepath.Join(out, "index.html")); !os.IsNotExist(err) {
		t.Fatal("oversized HTML saved")
	}
}

func TestWriteOutputFileDoesNotFollowEscapingSymlinks(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, target, file string }{
		{"linked-dir", outside, "linked-dir/keep.txt"},
		{"linked-file", target, "linked-file"},
	} {
		if err := os.Symlink(tc.target, filepath.Join(out, tc.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeOutputFile(out, filepath.Join(out, tc.file), []byte("overwrite")); err == nil {
			t.Errorf("allowed write through %s", tc.name)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("outside file modified: %s", data)
	}
}

func TestFetcherRetriesTransientServerFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	f := NewFetcherWithContext(nil, 1)
	defer f.Cancel()
	result, err := f.FetchURL(srv.URL)
	if err != nil || !result.OK || string(result.Body) != "ok" || hits.Load() != 2 {
		t.Fatalf("retry failed: result=%+v err=%v hits=%d", result, err, hits.Load())
	}
}

func TestInterruptedAssetDoesNotSavePartialFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short"))
	}))
	defer srv.Close()
	f := NewFetcher()
	defer f.Cancel()
	out := t.TempDir()
	if _, ok := downloadAssetFileWithLimit(f, out, srv.URL+"/partial.bin", 0, nil); ok {
		t.Fatal("truncated response marked successful")
	}
	if _, err := os.Stat(filepath.Join(out, "partial.bin")); !os.IsNotExist(err) {
		t.Fatal("partial asset left on disk")
	}
}

func TestDownloadCookieStaysAtStartingOrigin(t *testing.T) {
	var externalCookie atomic.Bool
	var localCookie atomic.Bool
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			externalCookie.Store(true)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("image"))
	}))
	defer external.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "test_session=synthetic" {
			localCookie.Store(true)
		}
		if r.URL.Path == "/redirect.png" {
			http.Redirect(w, r, external.URL+"/redirected.png", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<img src="` + external.URL + `/direct.png"><img src="/redirect.png">`))
	}))
	defer origin.Close()
	_, err := Download(Options{URL: origin.URL, OutputDir: t.TempDir(), Cookie: "test_session=synthetic", IncludeExternalAssets: true}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if !localCookie.Load() {
		t.Fatal("starting origin did not receive cookie")
	}
	if externalCookie.Load() {
		t.Fatal("cookie leaked to an external origin or redirect")
	}
}
