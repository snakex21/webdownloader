package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// bigSite serves many linked pages, each with assets and a script full of
// path literals, so a run spends its time in every crawler phase.
func bigSite(t *testing.T, slow time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slow)
		switch {
		case strings.HasSuffix(r.URL.Path, ".js"):
			var b strings.Builder
			for i := 0; i < 200; i++ {
				fmt.Fprintf(&b, "const a%d = 'img/pic%d.png'; ", i, i)
			}
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, b.String())
		case strings.HasSuffix(r.URL.Path, ".png"):
			fmt.Fprint(w, "PNG")
		default:
			w.Header().Set("Content-Type", "text/html")
			var b strings.Builder
			b.WriteString(`<script src="/app.js"></script><img src="/logo.png">`)
			for i := 0; i < 50; i++ {
				fmt.Fprintf(&b, `<a href="/p%s-%d.html">x</a>`, strings.ReplaceAll(r.URL.Path, "/", "_"), i)
			}
			fmt.Fprint(w, b.String())
		}
	}))
}

func runAsync(opts Options) (<-chan Summary, *atomic.Bool) {
	done := make(chan Summary, 1)
	completed := &atomic.Bool{}
	go func() {
		s, _ := Download(opts, Events{OnComplete: func(Summary) { completed.Store(true) }})
		done <- s
	}()
	return done, completed
}

func waitCancelled(t *testing.T, done <-chan Summary, completed *atomic.Bool, limit time.Duration) Summary {
	t.Helper()
	select {
	case s := <-done:
		if !s.Cancelled {
			t.Errorf("summary not marked cancelled: %+v", s)
		}
		if !completed.Load() {
			t.Error("OnComplete was not called after cancel")
		}
		return s
	case <-time.After(limit):
		t.Fatalf("Download did not stop within %v of cancel", limit)
	}
	return Summary{}
}

func TestCancelWhileRunning(t *testing.T) {
	srv := bigSite(t, 5*time.Millisecond)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done, completed := runAsync(Options{Context: ctx, URL: srv.URL + "/", OutputDir: t.TempDir(), MaxDepth: 5})
	time.Sleep(700 * time.Millisecond)
	cancel()
	waitCancelled(t, done, completed, 2*time.Second)
}

func TestCancelWhilePaused(t *testing.T) {
	srv := bigSite(t, 5*time.Millisecond)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	pause := NewPauseController()
	done, completed := runAsync(Options{Context: ctx, Pause: pause, URL: srv.URL + "/", OutputDir: t.TempDir(), MaxDepth: 5})
	time.Sleep(500 * time.Millisecond)
	pause.Pause()
	time.Sleep(500 * time.Millisecond)
	cancel()
	waitCancelled(t, done, completed, 2*time.Second)
}

// A server that sends headers and a little data, then stalls forever (like a
// streaming feed): cancel must not wait for the 30 s idle timeout.
func TestCancelDuringStalledRead(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a href="/feed.xml">rss</a><img src="/big.bin">`)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Length", "100000000")
		w.Write([]byte(strings.Repeat("x", 1024)))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	out := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done, completed := runAsync(Options{Context: ctx, URL: srv.URL + "/", OutputDir: out, MaxDepth: 2, DownloadAll: true})
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	waitCancelled(t, done, completed, 2*time.Second)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %v", d)
	}
	_ = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, partialSuffix) {
			t.Errorf("partial file left behind: %s", p)
		}
		return nil
	})
}

// While one slow file downloads, the ticker must still report received
// bytes and the file name (the UI showed a frozen 0 % before).
func TestProgressTickerDuringSlowFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a href="/video.mp4">v</a>`)
			return
		}
		chunk := make([]byte, 32*1024)
		for i := 0; i < 40; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()

	var maxTransferred atomic.Int64
	var sawCurrent atomic.Bool
	_, err := Download(Options{URL: srv.URL + "/", OutputDir: t.TempDir(), DownloadAll: true}, Events{
		OnPage: func(ev PageEvent) {
			if ev.Status != "progress" {
				return
			}
			if ev.Transferred > maxTransferred.Load() {
				maxTransferred.Store(ev.Transferred)
			}
			if strings.HasSuffix(ev.Current, "/video.mp4") && ev.Transferred > 0 {
				sawCurrent.Store(true)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxTransferred.Load() < 64*1024 {
		t.Errorf("ticker reported only %d transferred bytes", maxTransferred.Load())
	}
	if !sawCurrent.Load() {
		t.Error("ticker never reported the file being downloaded")
	}
}
