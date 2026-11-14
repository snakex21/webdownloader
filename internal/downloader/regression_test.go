package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// routeAllHostsTo sends every request (any host name) to srv, so a test can
// use several real-looking host names.
func routeAllHostsTo(t *testing.T, srv *httptest.Server) (port string) {
	t.Helper()
	port = srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, "127.0.0.1:"+port)
	}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	return port
}

// H1: a run that cannot create its output folder still ends with exactly
// one OnComplete carrying the reason.
func TestDownloadCompletesWhenOutputDirCannotBeCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var completes []Summary
	_, err := Download(Options{URL: "https://example.com/", OutputDir: filepath.Join(blocker, "out")},
		Events{OnComplete: func(s Summary) { completes = append(completes, s) }})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(completes) != 1 {
		t.Fatalf("OnComplete called %d times, want 1", len(completes))
	}
	if s := completes[0]; !s.Failed || s.FailKind != "write" || s.FailDetail == "" {
		t.Fatalf("summary = %+v, want a failed write", s)
	}
}

func TestDownloadInvalidURLStillCompletes(t *testing.T) {
	var got []Summary
	_, err := Download(Options{URL: "ftp://example.com/", OutputDir: filepath.Join(t.TempDir(), "x")},
		Events{OnComplete: func(s Summary) { got = append(got, s) }})
	if !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || !got[0].Failed || got[0].FailKind != "invalid" {
		t.Fatalf("OnComplete = %+v", got)
	}
}

// H1: no progress tick may arrive after OnComplete. OnComplete is slow here
// (as a UI dispatch can be), which used to leave the ticker running.
func TestNoProgressAfterComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<img src="/slow.png">`)
			return
		}
		time.Sleep(700 * time.Millisecond) // let the ticker run
		fmt.Fprint(w, "PNG")
	}))
	defer srv.Close()

	var mu sync.Mutex
	var order []string
	completed := false
	_, err := Download(Options{URL: srv.URL + "/", OutputDir: t.TempDir()}, Events{
		OnPage: func(ev PageEvent) {
			mu.Lock()
			defer mu.Unlock()
			if completed {
				order = append(order, "LATE:"+ev.Status)
				return
			}
			order = append(order, ev.Status)
		},
		OnComplete: func(Summary) {
			mu.Lock()
			completed = true
			order = append(order, "complete")
			mu.Unlock()
			time.Sleep(1200 * time.Millisecond)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(order) == 0 || order[len(order)-1] != "complete" {
		t.Fatalf("events after complete: %v", order)
	}
	if strings.Count(strings.Join(order, ","), "complete") != 1 {
		t.Fatalf("complete not exactly once: %v", order)
	}
}

// H2: the same path on different hosts is saved to different files; the
// start site keeps its layout and www/apex stay one site.
func TestSubdomainAndCDNFilesDoNotCollide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		port := strings.Split(r.Host, ":")[1]
		switch {
		case r.URL.Path == "/lib/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, "// js from "+host)
		case host == "blog.site.test":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>blog home</p><script src="/lib/app.js"></script>`)
		case r.URL.Path == "/about":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>about</p>`)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, strings.ReplaceAll(`<p>site home</p>
				<script src="/lib/app.js"></script>
				<script src="http://cdn.other.test:PORT/lib/app.js"></script>
				<a href="http://blog.site.test:PORT/">blog</a>
				<a href="http://site.test:PORT/about">about</a>`, "PORT", port))
		}
	}))
	defer srv.Close()
	port := routeAllHostsTo(t, srv)

	out := t.TempDir()
	sum, err := Download(Options{
		URL: "http://www.site.test:" + port + "/", OutputDir: out, MaxDepth: 2,
		IncludeSubdomains: true, IncludeExternalAssets: true,
	}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Pages != 3 {
		t.Errorf("pages = %d, want 3 (home, blog home, apex about)", sum.Pages)
	}
	want := map[string]string{
		"index.html":                       "site home",
		"about/index.html":                 "about",
		"_hosts/blog.site.test/index.html": "blog home",
		"lib/app.js":                       "js from www.site.test",
		"_hosts/cdn.other.test/lib/app.js": "js from cdn.other.test",
		"_hosts/blog.site.test/lib/app.js": "js from blog.site.test",
	}
	for rel, text := range want {
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil || !strings.Contains(string(data), text) {
			t.Errorf("%s = %q, %v; want it to contain %q", rel, data, err, text)
		}
	}
	home, _ := os.ReadFile(filepath.Join(out, "index.html"))
	for _, ref := range []string{`src="lib/app.js"`, `src="_hosts/cdn.other.test/lib/app.js"`, `href="_hosts/blog.site.test/index.html"`, `href="about/index.html"`} {
		if !strings.Contains(string(home), ref) {
			t.Errorf("home page lacks %s:\n%s", ref, home)
		}
	}
	blog, _ := os.ReadFile(filepath.Join(out, "_hosts", "blog.site.test", "index.html"))
	if !strings.Contains(string(blog), `src="lib/app.js"`) {
		t.Errorf("blog page script not rewritten to its own copy:\n%s", blog)
	}
}

// H3: relative links of a redirected page resolve against the final URL.
func TestRedirectedPageResolvesLinksAgainstFinalURL(t *testing.T) {
	var wrongHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/docs/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="intro.html">intro</a><img src="logo.png">`)
	})
	mux.HandleFunc("/docs/intro.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<p>intro</p>`)
	})
	mux.HandleFunc("/docs/logo.png", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "PNG") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wrongHits.Add(1)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	sum, err := Download(Options{URL: srv.URL + "/docs", OutputDir: out, MaxDepth: 2}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if n := wrongHits.Load(); n != 0 || sum.Errors != 0 {
		t.Errorf("links resolved against /docs instead of /docs/: %d wrong requests, %d errors", n, sum.Errors)
	}
	for _, rel := range []string{"docs/index.html", "docs/intro.html", "docs/logo.png"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s missing", rel)
		}
	}
	page, _ := os.ReadFile(filepath.Join(out, "docs", "index.html"))
	if !strings.Contains(string(page), `href="intro.html"`) || !strings.Contains(string(page), `src="logo.png"`) {
		t.Errorf("links not rewritten relative to docs/:\n%s", page)
	}
}

// H4: a large extension-less link that is not HTML goes to disk without
// being held in memory.
func TestLargeNonHTMLLinkIsStreamed(t *testing.T) {
	const size = 48 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a href="/download?id=5">get</a>`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(w, zeroReader{}, size)
	}))
	defer srv.Close()

	out := t.TempDir()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	sum, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 2, PageDelay: -1}, Events{})
	runtime.ReadMemStats(&after)
	if err != nil || sum.Failed {
		t.Fatalf("download: %v %+v", err, sum)
	}
	info, err := os.Stat(filepath.Join(out, "download__id_5"))
	if err != nil || info.Size() != size {
		t.Fatalf("saved file: %v, %v", info, err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > size/2 {
		t.Errorf("allocated %d MB for a %d MB file: body was buffered", alloc>>20, size>>20)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// H4: HTML is recognised by sniffing when the type says nothing, and its
// size in memory is capped.
func TestFetchPageOrFileSniffsAndCapsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		switch r.URL.Path {
		case "/page":
			fmt.Fprint(w, "  <!DOCTYPE html><html><body>hi</body></html>")
		case "/big":
			fmt.Fprint(w, "<!doctype html>"+strings.Repeat("x", 4096))
		default:
			fmt.Fprint(w, "PK\x03\x04 binary")
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	f := NewFetcher()

	res, err := f.FetchPageOrFile(srv.URL+"/page", 0, dir, filepath.Join(dir, "page"))
	if err != nil || res.Streamed || !res.IsHTML() || !strings.Contains(string(res.Body), "hi") {
		t.Fatalf("sniffed HTML: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "page")); !os.IsNotExist(err) {
		t.Error("HTML page was also written as a file")
	}

	res, err = f.FetchPageOrFile(srv.URL+"/file.zip", 0, dir, filepath.Join(dir, "file.zip"))
	if err != nil || !res.Streamed || res.Body != nil {
		t.Fatalf("binary: %+v, %v", res, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "file.zip")); string(data) != "PK\x03\x04 binary" {
		t.Errorf("streamed file = %q", data)
	}

	if _, err := f.FetchPageOrFile(srv.URL+"/big", 1024, dir, ""); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("oversized HTML: err = %v", err)
	}
}

// H5: a resumed run finishes the CSS an interrupted run saved but never
// rewrote: its url() assets are fetched and the file points at them.
func TestResumeRewritesCSSFromPreviousRun(t *testing.T) {
	var cssHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<link rel="stylesheet" href="/css/site.css"><p>home</p>`)
		case "/css/site.css":
			cssHits.Add(1)
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, rawCSS)
		case "/img/bg.png", "/img/icon.png", "/css/more.css":
			if strings.HasSuffix(r.URL.Path, ".css") {
				w.Header().Set("Content-Type", "text/css")
				fmt.Fprint(w, `.m{background:url(/img/icon.png)}`)
				return
			}
			fmt.Fprint(w, "PNG")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out := t.TempDir()
	// What an interrupted v2.1.0 run leaves: the stylesheet saved as served.
	cssFile := filepath.Join(out, "css", "site.css")
	if err := os.MkdirAll(filepath.Dir(cssFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cssFile, []byte(rawCSS), 0o644); err != nil {
		t.Fatal(err)
	}

	sum, err := Download(Options{URL: srv.URL + "/", OutputDir: out}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if cssHits.Load() != 0 {
		t.Errorf("complete stylesheet downloaded again (%d)", cssHits.Load())
	}
	if sum.Errors != 0 {
		t.Errorf("errors: %+v", sum.FailedFiles)
	}
	for _, rel := range []string{"img/bg.png", "img/icon.png", "css/more.css"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s not fetched on resume", rel)
		}
	}
	css, _ := os.ReadFile(cssFile)
	if !strings.Contains(string(css), "url(../img/bg.png)") || !strings.Contains(string(css), "more.css") || strings.Contains(string(css), "url(/img/") {
		t.Errorf("stylesheet not rewritten:\n%s", css)
	}
	more, _ := os.ReadFile(filepath.Join(out, "css", "more.css"))
	if !strings.Contains(string(more), "url(../img/icon.png)") {
		t.Errorf("nested stylesheet not rewritten:\n%s", more)
	}

	// Resuming again changes nothing and fetches nothing new.
	before := string(css)
	sum, err = Download(Options{URL: srv.URL + "/", OutputDir: out}, Events{})
	if err != nil || sum.Errors != 0 {
		t.Fatalf("second resume: %v %+v", err, sum.FailedFiles)
	}
	if css, _ := os.ReadFile(cssFile); string(css) != before {
		t.Errorf("second resume changed the stylesheet:\n%s", css)
	}
}

const rawCSS = `@import url("/css/more.css");
body { background: url(/img/bg.png); }
.icon { background: url(../img/icon.png); }`

// M1: fragments and "/" vs "/index.html" variants are queued once, so the
// queue length (and the UI percentage) is exact.
func TestQueueCountsEachPageOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<a href="/a#x">1</a><a href="/a#y">2</a><a href="/a/">3</a><a href="/A">4</a>
				<a href="/index.html">5</a><a href="#top">6</a><a href="/b?q=1#z">7</a><a href="/b?q=1">8</a>`)
			return
		}
		fmt.Fprint(w, "<p>page</p>")
	}))
	defer srv.Close()

	maxQueued := 0
	var urls []string
	sum, err := Download(Options{URL: srv.URL + "/", OutputDir: t.TempDir(), MaxDepth: 2, PageDelay: -1}, Events{
		OnPage: func(ev PageEvent) {
			maxQueued = max(maxQueued, ev.Queued)
			if ev.Status == "downloading" {
				urls = append(urls, ev.URL)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxQueued != 2 {
		t.Errorf("max queued = %d, want 2 (/a and /b?q=1)", maxQueued)
	}
	if sum.Pages != 3 {
		t.Errorf("pages = %d, want 3: %v", sum.Pages, urls)
	}
	for _, u := range urls {
		if strings.Contains(u, "#") {
			t.Errorf("fetched a URL with a fragment: %s", u)
		}
	}
}

// M8: names with '#', '%' or spaces are linked percent-encoded.
func TestHrefsArePercentEncoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a href="/a%23b.html">1</a><a href="/100%25.html">2</a><img src="/img/x%20y.png">`)
		case "/img/x y.png":
			fmt.Fprint(w, "PNG")
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<p>page</p>")
		}
	}))
	defer srv.Close()
	out := t.TempDir()
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 2, PageDelay: -1}, Events{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a#b.html", "100%.html", filepath.Join("img", "x y.png")} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s not saved", name)
		}
	}
	home, _ := os.ReadFile(filepath.Join(out, "index.html"))
	for _, ref := range []string{`href="a%23b.html"`, `href="100%25.html"`, `src="img/x%20y.png"`} {
		if !strings.Contains(string(home), ref) {
			t.Errorf("index.html lacks %s:\n%s", ref, home)
		}
	}
}

// M9: over-long path segments are shortened deterministically, and links
// point at the shortened names.
func TestLongPathSegmentsAreShortened(t *testing.T) {
	base := t.TempDir()
	slug := strings.Repeat("zażółć-gęślą-jaźń-", 20)
	p1, err1 := FilePathFor(base, "https://example.com/blog/"+slug+"1/")
	p2, err2 := FilePathFor(base, "https://example.com/blog/"+slug+"2/")
	p1again, _ := FilePathFor(base, "https://example.com/blog/"+slug+"1/")
	a1, err3 := AssetPathFor(base, "https://example.com/img/"+slug+".png")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatal(err1, err2, err3)
	}
	if p1 == p2 || p1 != p1again {
		t.Fatalf("not unique/deterministic: %s %s", p1, p2)
	}
	for _, p := range []string{p1, p2, a1} {
		rel, _ := filepath.Rel(base, p)
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if len(seg) > maxSegmentLen {
				t.Errorf("segment of %d bytes: %s", len(seg), seg)
			}
		}
	}
	if !strings.HasSuffix(a1, ".png") {
		t.Errorf("extension lost: %s", a1)
	}
	rel, err := RelativePagePath(base, "https://example.com/", "https://example.com/blog/"+slug+"1/")
	if err != nil {
		t.Fatal(err)
	}
	relFile, _ := filepath.Rel(base, p1)
	if want := escapeRelPath(filepath.ToSlash(relFile)); rel != want {
		t.Errorf("link %q does not match saved file %q", rel, want)
	}
}

// M4: waiting for the WebView2 page target stops as soon as the run is
// cancelled.
func TestFirstPageTargetStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	started := time.Now()
	_, err := firstPageTarget(ctx, "ws://"+strings.TrimPrefix(srv.URL, "http://")+"/devtools/browser/x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("took %s after cancel", d)
	}
}
