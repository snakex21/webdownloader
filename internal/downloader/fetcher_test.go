package downloader

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testFetcher(ctx context.Context, retries int, timeout time.Duration) *Fetcher {
	f := NewFetcherWithContext(ctx, retries)
	f.timeout = timeout
	f.backoff = 20 * time.Millisecond
	return f
}

func TestFetcherSlowButSteadyBodyIsNotTimedOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 8; i++ {
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()
	// Total transfer (~400ms) is far longer than the timeout (150ms), but
	// data keeps flowing, so the old whole-request deadline must not apply.
	f := testFetcher(context.Background(), 0, 150*time.Millisecond)
	defer f.Cancel()
	res, err := f.FetchURL(srv.URL)
	if err != nil || len(res.Body) != 40 {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestFetcherStalledBodyTimesOutAndRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 1, 100*time.Millisecond)
	defer f.Cancel()
	_, err := f.FetchURL(srv.URL)
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (timeout must be retried)", hits.Load())
	}
}

func TestFetcherStalledHeadersTimeOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 0, 100*time.Millisecond)
	defer f.Cancel()
	start := time.Now()
	_, err := f.FetchURL(srv.URL)
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not applied while waiting for headers")
	}
}

func TestFetcherRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantHits  int32
		retryable bool
	}{
		{http.StatusTooManyRequests, 3, true},
		{http.StatusRequestTimeout, 3, true},
		{http.StatusBadGateway, 3, true},
		{http.StatusNotFound, 1, false},
		{http.StatusForbidden, 1, false},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			f := testFetcher(context.Background(), 2, time.Second)
			defer f.Cancel()
			res, err := f.FetchURL(srv.URL)
			if err != nil || res.OK || res.Status != tc.status {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			if hits.Load() != tc.wantHits {
				t.Fatalf("hits = %d, want %d", hits.Load(), tc.wantHits)
			}
		})
	}
}

func TestFetcherBackoffIsExponentialAndHonoursRetryAfter(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		n := len(times)
		mu.Unlock()
		switch n {
		case 1, 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 3:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 3, time.Second)
	f.backoff = 80 * time.Millisecond
	defer f.Cancel()
	res, err := f.FetchURL(srv.URL)
	if err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	gap := func(i int) time.Duration { return times[i].Sub(times[i-1]) }
	if gap(1) < 80*time.Millisecond || gap(2) < 160*time.Millisecond {
		t.Errorf("back-off not exponential: %v, %v", gap(1), gap(2))
	}
	if gap(3) < 900*time.Millisecond {
		t.Errorf("Retry-After ignored: waited %v", gap(3))
	}
}

func TestFetcherCancelStopsRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	f := testFetcher(ctx, 5, time.Second)
	f.backoff = 200 * time.Millisecond
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := f.FetchURL(srv.URL)
	if !errors.Is(err, context.Canceled) || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestFetcherFollowsRedirectsAndStopsLoops(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("moved")) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := testFetcher(context.Background(), 0, time.Second)
	defer f.Cancel()

	out := t.TempDir()
	res, err := f.FetchToFile(srv.URL+"/old", 0, out, filepath.Join(out, "old"))
	if err != nil || !res.OK || res.FinalURL != srv.URL+"/new" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := f.FetchURL(srv.URL + "/loop"); err == nil {
		t.Fatal("redirect loop not stopped")
	}
}

func TestFetchToFileStreamsLargeBodyWithoutBuffering(t *testing.T) {
	const size = 64 << 20
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		for written := 0; written < size; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 0, 5*time.Second)
	defer f.Cancel()
	out := t.TempDir()
	target := filepath.Join(out, "big.bin")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := f.FetchToFile(srv.URL, 0, out, target)
	runtime.ReadMemStats(&after)
	if err != nil || res.Size != size || res.Body != nil {
		t.Fatalf("size=%d body=%d err=%v", res.Size, len(res.Body), err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > size/4 {
		t.Fatalf("allocated %d bytes for a %d-byte file; body is being buffered", alloc, size)
	}
	if info, err := os.Stat(target); err != nil || info.Size() != size {
		t.Fatalf("stat: %v %v", info, err)
	}
}

func TestFetchToFileSizeLimitWithoutContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ {
			w.Write(bytes.Repeat([]byte("y"), 1000))
			w.(http.Flusher).Flush() // forces chunked encoding, no Content-Length
		}
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 2, time.Second)
	defer f.Cancel()
	out := t.TempDir()
	target := filepath.Join(out, "big.bin")
	if _, err := f.FetchToFile(srv.URL, 5000, out, target); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("oversized file left on disk")
	}
	assertNoPartialFiles(t, out)
}

func TestFetchToFileCancelMidDownloadRemovesPartialFile(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write(bytes.Repeat([]byte("z"), 10000))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	f := testFetcher(ctx, 2, 5*time.Second)
	go func() { <-started; time.Sleep(50 * time.Millisecond); cancel() }()
	out := t.TempDir()
	target := filepath.Join(out, "file.bin")
	if _, err := f.FetchToFile(srv.URL, 0, out, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("partial file left under final name")
	}
	assertNoPartialFiles(t, out)
}

func TestFetchToFileNotFoundWritesNothing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f := testFetcher(context.Background(), 2, time.Second)
	defer f.Cancel()
	out := t.TempDir()
	res, err := f.FetchToFile(srv.URL+"/missing.png", 0, out, filepath.Join(out, "missing.png"))
	if err != nil || res.OK || res.Status != 404 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Fatalf("files written for a 404: %v", entries)
	}
}

func TestFetchToFileLocalWriteErrorIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("data"))
	}))
	defer srv.Close()
	f := testFetcher(context.Background(), 2, time.Second)
	defer f.Cancel()
	out := t.TempDir()
	target := filepath.Join(out, "taken")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := f.FetchToFile(srv.URL, 0, out, target)
	var wErr *localWriteError
	if !errors.As(err, &wErr) {
		t.Fatalf("err = %v, want local write error", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d; disk errors must not be retried", hits.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("3"); got != 3*time.Second {
		t.Errorf("seconds: %v", got)
	}
	if got := parseRetryAfter(time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)); got < 3*time.Second || got > 6*time.Second {
		t.Errorf("http-date: %v", got)
	}
	for _, v := range []string{"", "-1", "soon"} {
		if got := parseRetryAfter(v); got != 0 {
			t.Errorf("%q: %v", v, got)
		}
	}
}
