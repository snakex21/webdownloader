package downloader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestScanJS(t *testing.T) {
	src := `
import { a } from './wm.js?v=3';
import "./side.js";
export * from '../shared/util.js';
import bare from 'lodash';
const m = await import('./lazy.js');
const icons = new URL('../icons/icons.svg', import.meta.url);
fetch('data/games.json').then(r => r.json());
const games = [{ url: "wojna_er/index.html" }, { url: "gra zapamietywanie/gra.html" }];
const dyn = ` + "`themes/${name}.css`" + `;
const notPath = "image/png", word = "hello.world";
`
	got := ScanJS(src)
	var modules, docs []string
	for _, r := range got {
		if r.Kind == JSRefModule {
			modules = append(modules, r.URL)
		} else {
			docs = append(docs, r.URL)
		}
	}
	for _, want := range []string{"./wm.js?v=3", "./side.js", "../shared/util.js", "./lazy.js", "../icons/icons.svg"} {
		if !slices.Contains(modules, want) {
			t.Errorf("module ref %q missing (got %v)", want, modules)
		}
	}
	if slices.Contains(modules, "lodash") {
		t.Error("bare specifier must be left to the import map")
	}
	for _, want := range []string{"data/games.json", "wojna_er/index.html", "gra zapamietywanie/gra.html"} {
		if !slices.Contains(docs, want) {
			t.Errorf("document ref %q missing (got %v)", want, docs)
		}
	}
	for _, bad := range []string{"image/png", "hello.world", "./wm.js?v=3"} {
		if slices.Contains(docs, bad) {
			t.Errorf("unexpected document ref %q", bad)
		}
	}
	for _, r := range got {
		if strings.Contains(r.URL, "${") {
			t.Errorf("template literal leaked: %q", r.URL)
		}
	}
}

func TestScanJSON(t *testing.T) {
	got := ScanJSON([]byte(`{"themes":[{"file":"win95.css","name":"Windows 95"}],"icon":"icons/a.png","n":3}`))
	slices.Sort(got)
	if want := []string{"icons/a.png", "win95.css"}; !slices.Equal(got, want) {
		t.Fatalf("ScanJSON = %v, want %v", got, want)
	}
}

func TestRewriteImportMapKeepsFormatting(t *testing.T) {
	src := "{\n \"imports\": {\n  \"./js/a.js\": \"./js/a.js?v=9\",\n  \"x\": \"/abs/x.js\"\n }\n}"
	out := RewriteImportMap(src, map[string]string{"/abs/x.js": "./abs/x.js"})
	if !strings.Contains(out, `"x": "./abs/x.js"`) || !strings.Contains(out, `"./js/a.js": "./js/a.js?v=9"`) {
		t.Fatalf("unexpected rewrite:\n%s", out)
	}
	targets := ImportMapTargets(src)
	slices.Sort(targets)
	if want := []string{"./js/a.js?v=9", "/abs/x.js"}; !slices.Equal(targets, want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
}

// moduleSite is a small site in the style of modern static pages: an import
// map with ?v= versions, ES modules, fetch()ed JSON and pages that are only
// listed in a JS data file.
func moduleSite(t *testing.T) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/": `<!doctype html><html><head>
<link rel="stylesheet" href="css/base.css?v=3">
<script type="importmap">{"imports": {"./js/main.js": "./js/main.js?v=3", "./js/wm.js": "./js/wm.js?v=3"}}</script>
<script type="module" src="js/main.js?v=3"></script>
<script src="js/games-data.js"></script>
<script src="https://cdn.example.net/lib.js"></script>
</head><body><a href="https://other.example/">x</a></body></html>`,
		"/css/base.css":     `@import url("print.css") print; body{background:url(../img/bg.png?v=3)}`,
		"/css/print.css":    `body{color:#000}`,
		"/img/bg.png":       "PNG",
		"/js/main.js":       `import { open } from './wm.js'; fetch('data/list.json');`,
		"/js/wm.js":         `export const open = () => new URL('../icons/icons.svg', import.meta.url);`,
		"/js/games-data.js": `const games = [{ url: "game/index.html", cover: "game/cover.jpg" }];`,
		"/icons/icons.svg":  `<svg/>`,
		"/data/list.json":   `{"items": [{"icon": "img/item.png"}]}`,
		"/img/item.png":     "PNG",
		"/game/index.html":  `<html><body><canvas></canvas><script src="game.js"></script></body></html>`,
		"/game/game.js":     `console.log(1)`,
		"/game/cover.jpg":   "JPG",
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "text/javascript")
		case strings.HasSuffix(r.URL.Path, ".css"):
			w.Header().Set("Content-Type", "text/css")
		case strings.HasSuffix(r.URL.Path, ".json"):
			w.Header().Set("Content-Type", "application/json")
		}
		fmt.Fprint(w, body)
	}))
}

func TestDownload_ModuleSiteOffline(t *testing.T) {
	srv := moduleSite(t)
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "site")
	sum, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 2}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"index.html", "css/base.css", "css/print.css", "img/bg.png", "js/main.js", "js/wm.js",
		"js/games-data.js", "icons/icons.svg", "data/list.json", "img/item.png",
		"game/index.html", "game/game.js", "game/cover.jpg",
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(f))); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if sum.Pages != 2 {
		t.Errorf("pages = %d, want 2 (start + game found in games-data.js)", sum.Pages)
	}
	// main.js is referenced with and without ?v=3 but stored once.
	if sum.Assets != 11 {
		t.Errorf("assets = %d, want 11", sum.Assets)
	}
	if !sum.NeedsServer {
		t.Error("module site must be flagged as needing the preview server")
	}
	if sum.SkippedExternal != 1 || !slices.Equal(sum.SkippedExternalHosts, []string{"cdn.example.net"}) {
		t.Errorf("skipped external = %d %v", sum.SkippedExternal, sum.SkippedExternalHosts)
	}
	if sum.Errors != 0 {
		t.Errorf("errors = %d, want 0 (speculative misses are silent)", sum.Errors)
	}
	html, _ := os.ReadFile(filepath.Join(out, "index.html"))
	for _, want := range []string{`href="css/base.css?v=3"`, `src="js/main.js?v=3"`, `"./js/main.js": "./js/main.js?v=3"`} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html lacks %s:\n%s", want, html)
		}
	}
	css, _ := os.ReadFile(filepath.Join(out, "css", "base.css"))
	if !strings.Contains(string(css), `@import url(print.css) print;`) || !strings.Contains(string(css), `url(../img/bg.png?v=3)`) {
		t.Errorf("css not rewritten correctly: %s", css)
	}
	if sum.StartFile != filepath.Join(out, "index.html") {
		t.Errorf("StartFile = %q", sum.StartFile)
	}
}

func TestDownload_FailedStartPageLeavesNoFolder(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "site")
	var completed Summary
	sum, err := Download(Options{URL: srv.URL + "/missing", OutputDir: out}, Events{
		OnComplete: func(s Summary) { completed = s },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Failed || sum.FailKind != "http" || sum.FailStatus != 404 || !completed.Failed {
		t.Fatalf("summary = %+v", sum)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("empty output folder left behind (err=%v)", err)
	}
}

func TestDownload_UpdateRefetchesExistingFiles(t *testing.T) {
	version := "one"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			fmt.Fprint(w, version)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<script src="app.js"></script>`)
	}))
	defer srv.Close()
	out := t.TempDir()
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out, SkipExisting: true}, Events{}); err != nil {
		t.Fatal(err)
	}
	version = "two"
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out, SkipExisting: true}, Events{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "app.js")); string(b) != "one" {
		t.Fatalf("resume must reuse files, got %q", b)
	}
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: out, Refresh: true}, Events{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "app.js")); string(b) != "two" {
		t.Fatalf("update must refetch files, got %q", b)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err    error
		kind   string
		status int
	}{
		{&HTTPStatusError{Status: 404}, "http", 404},
		{fmt.Errorf("fetch: %w", &net.DNSError{Err: "no such host", Name: "x.invalid"}), "dns", 0},
		{errors.New(`dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.`), "refused", 0},
		{fmt.Errorf("fetch: %w", ErrIdleTimeout), "timeout", 0},
		{errors.New("x509: certificate signed by unknown authority"), "tls", 0},
		{fmt.Errorf("x: %w", ErrNoBrowser), "browser", 0},
		{errors.New("weird"), "other", 0},
	}
	for _, c := range cases {
		kind, status := classifyError(c.err)
		if kind != c.kind || status != c.status {
			t.Errorf("classifyError(%v) = %q %d, want %q %d", c.err, kind, status, c.kind, c.status)
		}
	}
}

// Reproduces the skijumping.pl stall: an error message in a library looked
// like a file name, the server answered it with an HTML page (soft 404),
// the page was scanned as a script and every quoted path in it became a
// slow, silent 404 request.
func TestSpeculativeSoft404AndBrokenAssets(t *testing.T) {
	var requests atomic.Int64
	var brokenHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case r.URL.Path == "/lib.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `throw new Error("Popover requires tooltip.js"); var x = "js/soft.js";`)
		case r.URL.Path == "/js/soft.js":
			w.Header().Set("Content-Type", "text/html")
			var b strings.Builder
			b.WriteString("<!DOCTYPE html><html>")
			for i := 0; i < 300; i++ {
				fmt.Fprintf(&b, `<img src="pics/p%d.jpg">`, i)
			}
			fmt.Fprint(w, b.String())
		case r.URL.Path == "/broken.css":
			brokenHits.Add(1)
			http.Error(w, "nope", http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, ".jpg"):
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<link rel="stylesheet" href="/broken.css"><script src="/lib.js"></script><a href="/a.html">a</a><a href="/b.html">b</a>`)
		}
	}))
	defer srv.Close()
	out := t.TempDir()
	sum, err := Download(Options{URL: srv.URL + "/", OutputDir: out, MaxDepth: 2}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "js", "soft.js")); !os.IsNotExist(err) {
		t.Error("an HTML answer to a guessed .js URL must not be saved")
	}
	if n := requests.Load(); n > 12 {
		t.Errorf("%d requests: soft-404 page was scanned as a script", n)
	}
	if brokenHits.Load() > 1 {
		t.Errorf("broken asset requested %d times, want once per run", brokenHits.Load())
	}
	if sum.Errors != 1 {
		t.Errorf("errors = %d, want 1 (broken.css reported once)", sum.Errors)
	}
}

func TestLiteralWithSpacesNeedsDirectory(t *testing.T) {
	for v, want := range map[string]bool{
		"Popover requires tooltip.js":                false,
		"gra zapamietywanie/gra zapamietywanie.html": true,
		"img/a b.png": true,
	} {
		if got := looksLikeFilePath(v); got != want {
			t.Errorf("looksLikeFilePath(%q) = %v, want %v", v, got, want)
		}
	}
}

// A script full of strings that are not paths on this site stops being
// mined after guessGiveUp misses instead of costing hundreds of requests.
func TestGuessingGivesUpAfterMisses(t *testing.T) {
	var guesses atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			for i := 0; i < 400; i++ {
				fmt.Fprintf(w, "var g%d = 'gfx/item%d.png';\n", i, i)
			}
		case strings.HasPrefix(r.URL.Path, "/gfx/"):
			guesses.Add(1)
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<script src="/app.js"></script>`)
		}
	}))
	defer srv.Close()
	if _, err := Download(Options{URL: srv.URL + "/", OutputDir: t.TempDir()}, Events{}); err != nil {
		t.Fatal(err)
	}
	// Without the rule all 400 would be requested.
	if n := guesses.Load(); n > guessGiveUp || n < guessGiveUp/2 {
		t.Errorf("%d guessed requests, want about %d", n, guessGiveUp)
	}
}

func TestWWWAndApexAreOneSite(t *testing.T) {
	opts := Options{}
	for _, c := range []struct {
		host, base string
		want       bool
	}{
		{"skijumping.pl", "www.skijumping.pl", true},
		{"www.skijumping.pl", "skijumping.pl", true},
		{"WWW.Example.com.", "example.com", true},
		{"foto.skijumping.pl", "www.skijumping.pl", false},
		{"www.com", "com", false},
		{"get.optad360.io", "www.skijumping.pl", false},
	} {
		if got := sameScopeHost(c.host, c.base, opts); got != c.want {
			t.Errorf("sameScopeHost(%q, %q) = %v, want %v", c.host, c.base, got, c.want)
		}
	}
	if !sameScopeHost("foto.skijumping.pl", "www.skijumping.pl", Options{IncludeSubdomains: true}) {
		t.Error("subdomain option must still cover other subdomains")
	}
}

// Links and assets on the apex host of a www start URL are mirrored and
// rewritten like internal ones, not reported as external.
func TestApexLinksOfWWWSiteAreInternal(t *testing.T) {
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logo.png":
			fmt.Fprint(w, "PNG")
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, strings.ReplaceAll(`<img src="http://site.test:PORT/logo.png"><a href="http://site.test:PORT/about">about</a>`, "PORT", port))
		}
	}))
	defer srv.Close()
	port = srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	// Route both names to the test server.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, "127.0.0.1:"+port)
	}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	defer func() { http.DefaultTransport = old }()
	out := t.TempDir()
	sum, err := Download(Options{URL: "http://www.site.test:" + port + "/", OutputDir: out, MaxDepth: 2}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.SkippedExternal != 0 {
		t.Errorf("apex host reported as external: %v", sum.SkippedExternalHosts)
	}
	if sum.Pages != 2 {
		t.Errorf("pages = %d, want 2 (apex /about crawled)", sum.Pages)
	}
	if _, err := os.Stat(filepath.Join(out, "logo.png")); err != nil {
		t.Error("apex asset not downloaded")
	}
}
