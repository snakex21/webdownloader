package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

type PauseController struct{ paused atomic.Bool }

func NewPauseController() *PauseController { return &PauseController{} }
func (p *PauseController) Pause() {
	if p != nil {
		p.paused.Store(true)
	}
}
func (p *PauseController) Resume() {
	if p != nil {
		p.paused.Store(false)
	}
}
func (p *PauseController) IsPaused() bool { return p != nil && p.paused.Load() }
func (p *PauseController) Wait(ctx context.Context) error {
	for p != nil && p.paused.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil
}

// fileExtensions lists the extensions that count as "downloadable attachments"
// when DownloadAll is true. Mirrors the original Node implementation.
var fileExtensions = map[string]struct{}{
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {},
	".ppt": {}, ".pptx": {}, ".zip": {}, ".rar": {}, ".7z": {},
	".tar": {}, ".gz": {}, ".mp3": {}, ".mp4": {}, ".avi": {},
	".mov": {}, ".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {},
	".svg": {}, ".webp": {}, ".ico": {},
}

// Mode controls how pages are fetched. The default "http" mode is fast and
// uses the standard HTTP client. "browser" mode renders every page in a
// browser engine and saves everything the page loads.
type Mode string

const (
	ModeHTTP    Mode = "http"
	ModeBrowser Mode = "browser"
)

// Options configures a download run.
type Options struct {
	// Context cancels the whole crawl, including in-flight HTTP requests and
	// browser rendering. If nil, context.Background() is used.
	Context   context.Context
	Pause     *PauseController
	URL       string
	OutputDir string
	// MaxDepth is the number of page levels to save: 1 = the start page
	// only, 2 = start page + pages it links to, and so on (as in the UI).
	MaxDepth int
	// Mode selects the fetcher: "http" (default) or "browser".
	Mode Mode
	// Engine selects the browser for ModeBrowser (default: auto).
	Engine Engine
	// DownloadAll enables attachment downloads (PDF/ZIP/DOC/...).
	DownloadAll bool
	// IncludeSubdomains extends the on-domain filter to subdomains of the
	// start host (foo.example.com is allowed when the start host is
	// example.com).
	IncludeSubdomains bool
	// IncludeExternalAssets allows downloading assets whose host differs
	// from the page's host (CDN, font providers, etc.).
	IncludeExternalAssets bool
	// MaxPages stops the crawl after this many pages have been downloaded.
	// 0 means unlimited.
	MaxPages int
	// MaxTotalBytes stops the crawl once the total downloaded bytes
	// (HTML + assets + attachments) reach this value. 0 means unlimited.
	MaxTotalBytes int64
	// MaxFileBytes skips a single file whose body is larger than this
	// many bytes. 0 means unlimited.
	MaxFileBytes int64
	// Retries is the number of extra attempts for a failed HTTP request
	// (with a short back-off). Defaults to 2.
	Retries int
	Cookie  string
	// Refresh downloads every file again and overwrites it (updating an
	// existing copy). Without it, complete files already in OutputDir are
	// reused (resume).
	Refresh bool
	// PageDelay is the pause between pages (politeness). Default 250 ms;
	// negative means none.
	PageDelay time.Duration
}

// Summary reports the totals of a completed run.
type Summary struct {
	Pages       int
	Assets      int
	Attachments int
	Bytes       int64
	OutputDir   string
	Cancelled   bool
	ReportPath  string
	// StartFile is the saved start page (empty when it failed).
	StartFile string
	// Failed is true when not even the start page could be saved.
	Failed bool
	// FailKind classifies the failure for the UI: "http", "dns",
	// "refused", "timeout", "tls", "browser", "too_large", "invalid",
	// "write", "other". FailStatus is the HTTP status for "http".
	FailKind   string
	FailStatus int
	FailDetail string
	// Errors counts failed pages/assets (speculative misses excluded).
	Errors int
	// SkippedTrackers counts skipped references to ad, analytics and
	// consent hosts (normal, harmless offline).
	SkippedTrackers     int
	SkippedTrackerHosts []string
	// FailedFiles lists the first failed pages/files with a reason.
	FailedFiles []FailedFile
	// SkippedExternal counts references to other hosts (CDNs) that were
	// not downloaded because external assets are off.
	SkippedExternal      int
	SkippedExternalHosts []string
	// NeedsServer is true when the copy uses ES modules or fetch(), which
	// browsers block on file:// - open it through the offline preview.
	NeedsServer bool
	// Engine names the browser engine used in browser mode.
	Engine string
	// Elapsed is the wall time of the run.
	Elapsed time.Duration
}

// Events receives progress notifications from a running download. All
// callbacks are optional and must be fast (or push to a channel). They are
// never called concurrently; OnPage with Status "progress" comes from a
// ticker goroutine (about twice a second), everything else from the
// goroutine running Download. Every run ends with exactly one OnComplete
// (also when Download returns an error), and nothing is called after it.
type Events struct {
	OnPage     func(PageEvent)
	OnAsset    func(assetURL, kind string) // kind: "asset" | "attachment"
	OnError    func(pageURL string, err error)
	OnComplete func(Summary)
}

// PageEvent describes a single page lifecycle moment.
type PageEvent struct {
	URL         string
	Status      string // "downloading" | "completed" | "error" | "done"
	Depth       int
	Pages       int
	Assets      int
	Attachments int
	Bytes       int64
	// Queued is the number of pages waiting in the crawl queue.
	Queued int
	// Transferred counts every byte received from the network so far,
	// including files still being downloaded (for speed estimates).
	Transferred int64
	// Current is the file being downloaded right now.
	Current string
}

// pageFetcher is the contract Download uses to grab one page. The default
// HTTP implementation lives in fetcher.go; the "browser" implementation is
// in browser.go.
type pageFetcher interface {
	FetchPage(url string) (*FetchResult, error)
	Close() error
}

// maxSpeculative caps requests made for URLs guessed from script and JSON
// string literals, so a data-heavy script cannot explode a crawl; per page
// at most maxSpeculativePerPage new guesses are tried.
const (
	maxSpeculative        = 1500
	maxSpeculativePerPage = 150
)

// crawler holds the state of one Download run.
type crawler struct {
	opts    Options
	domain  string
	layout  layout
	ctx     context.Context
	http    *Fetcher
	fetcher pageFetcher
	events  Events
	seen    map[string]struct{}
	// Live counters, also read by the progress ticker.
	pagesDone   atomic.Int64
	attachments atomic.Int64
	queueLen    atomic.Int64
	current     atomic.Pointer[string]
	emitMu      sync.Mutex
	// seenPaths dedupes by output file: "app.js?v=7" and "app.js" are
	// saved to the same file and must be fetched and counted once.
	seenPaths  map[string]struct{}
	totalBytes atomic.Int64
	assetsN    atomic.Int64

	// Script/JSON literal discovery.
	jsLiterals       map[string][]string // script URL -> document-relative literals
	scanned          map[string]struct{}
	speculativeTried map[string]struct{}
	pageSpeculative  int
	// failed remembers assets that already failed in this run, so every
	// page linking the same broken file does not request it again.
	failed          map[string]struct{}
	guesses         map[string]*guessStat
	discoveredPages []string // pages found in literals for the current page

	skippedExternal map[string]int
	reportErrors    []map[string]string
	failedFiles     []FailedFile
	speculativeMiss int
	needsServer     bool

	// Run lifecycle, set up by Download (see run.go).
	browser         *browserFetcher // nil in HTTP mode
	started         time.Time
	startFile       string // where the start page is saved
	createdDir      bool   // OutputDir did not exist before the run
	errLog          *os.File
	summary         Summary // outcome flags; totals are filled by finish
	stopTicker      func()
	queue           pageQueue
	attachmentsSeen map[string]struct{}
}

// onlyRunMetadata reports whether dir holds nothing but the files Download
// itself creates at the start (so it can be removed after a failed start).
func onlyRunMetadata(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch e.Name() {
		case "download-errors.log", "download-report.json":
		default:
			return false
		}
	}
	return true
}

// pageKey identifies a page by the file it is saved to (case-insensitive,
// as on Windows), or by its canonical URL when it has no valid file path.
func (c *crawler) pageKey(pageURL string) string {
	if p, err := c.layout.pagePath(pageURL); err == nil {
		return "file:" + strings.ToLower(p)
	}
	return "url:" + canonicalURL(pageURL)
}

// canonicalURL drops the fragment and default port, lower-cases scheme and
// host and treats www.<domain> as <domain>.
func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme = strings.ToLower(u.Scheme)
	host, port := siteHost(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// maxFailedListed is how many failures the result lists by name.
const maxFailedListed = 20

// FailedFile is one failed page or file, with a classified reason.
type FailedFile struct {
	URL    string
	Kind   string // as Summary.FailKind
	Status int
}

// skippedTotals splits skipped external references into content hosts
// (CDNs - may matter offline) and ad/analytics/consent hosts (harmless).
func (c *crawler) skippedTotals() (content int, contentHosts []string, trackers int, trackerHosts []string) {
	for h, n := range c.skippedExternal {
		if IsTrackerHost(h) {
			trackers += n
			trackerHosts = append(trackerHosts, h)
		} else {
			content += n
			contentHosts = append(contentHosts, h)
		}
	}
	byCount := func(hosts []string) {
		sort.Slice(hosts, func(i, j int) bool {
			return c.skippedExternal[hosts[i]] > c.skippedExternal[hosts[j]] ||
				(c.skippedExternal[hosts[i]] == c.skippedExternal[hosts[j]] && hosts[i] < hosts[j])
		})
	}
	byCount(contentHosts)
	byCount(trackerHosts)
	return content, contentHosts, trackers, trackerHosts
}

func writeReport(path string, opts Options, summary Summary, errs []map[string]string, speculativeMiss int) {
	if errs == nil {
		errs = []map[string]string{}
	}
	report := map[string]any{
		"url":                  opts.URL,
		"outputDir":            opts.OutputDir,
		"mode":                 opts.Mode,
		"engine":               summary.Engine,
		"pages":                summary.Pages,
		"assets":               summary.Assets,
		"attachments":          summary.Attachments,
		"bytes":                summary.Bytes,
		"cancelled":            summary.Cancelled,
		"failed":               summary.Failed,
		"failKind":             summary.FailKind,
		"startFile":            summary.StartFile,
		"needsServer":          summary.NeedsServer,
		"skippedExternal":      summary.SkippedExternal,
		"skippedExternalHosts": summary.SkippedExternalHosts,
		"skippedTrackers":      summary.SkippedTrackers,
		"skippedTrackerHosts":  summary.SkippedTrackerHosts,
		"speculativeMisses":    speculativeMiss,
		"elapsedSeconds":       summary.Elapsed.Seconds(),
		"finishedAt":           time.Now().Format(time.RFC3339),
		"errors":               errs,
	}
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = writeOutputFile(opts.OutputDir, path, data)
	}
}

// shouldFollowPage reports whether the crawler may follow an HTML/link URL.
// It never uses IncludeExternalAssets: that flag is intentionally limited to
// assets (CDN fonts/images/scripts), not outbound page crawling.
func shouldFollowPage(absURL, baseDomain string, opts Options) bool {
	parsed, err := url.Parse(absURL)
	if err != nil {
		return false
	}
	if isTechnicalCrawlerURL(parsed) {
		return false
	}
	host := parsed.Hostname()
	return sameScopeHost(host, baseDomain, opts)
}

func isTechnicalCrawlerURL(parsed *url.URL) bool {
	path := strings.ToLower(parsed.EscapedPath())
	return path == "/cdn-cgi/l/email-protection" || strings.HasPrefix(path, "/cdn-cgi/l/email-protection/")
}

// shouldDownloadAsset reports whether an asset URL may be downloaded. Unlike
// shouldFollowPage, this DOES honour IncludeExternalAssets so CDN assets work
// without allowing arbitrary external HTML crawling.
func shouldDownloadAsset(absURL, baseDomain string, opts Options) bool {
	parsed, err := url.Parse(absURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if sameScopeHost(host, baseDomain, opts) {
		return true
	}
	return opts.IncludeExternalAssets
}

// sameScopeHost reports whether host belongs to the site being mirrored.
// "www.example.com" and "example.com" are the same site; other subdomains
// count only with IncludeSubdomains.
func sameScopeHost(host, baseDomain string, opts Options) bool {
	h, b := siteHost(host), siteHost(baseDomain)
	if h == b {
		return true
	}
	return opts.IncludeSubdomains && isSubdomain(h, b)
}

// siteHost lower-cases a host name and drops a trailing dot and a leading
// "www." label.
func siteHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if rest, ok := strings.CutPrefix(host, "www."); ok && strings.Contains(rest, ".") {
		return rest
	}
	return host
}

// isSubdomain reports whether host is a subdomain of base. base may be
// "example.com"; host may be "www.example.com", "a.b.example.com", etc.
// Equality is NOT considered a subdomain match — that is handled by the
// caller (so IncludeSubdomains is purely additive).
func isSubdomain(host, base string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	base = strings.ToLower(strings.TrimSuffix(base, "."))
	if host == base {
		return false
	}
	return strings.HasSuffix(host, "."+base)
}

// download fetches one asset into the mirror (reusing earlier copies as
// configured).
func (c *crawler) download(assetURL string) (downloadResult, error) {
	if p, err := c.layout.assetPath(assetURL); err == nil {
		key := strings.ToLower(p)
		if _, dup := c.seenPaths[key]; dup {
			c.seen[assetURL] = struct{}{}
			return downloadResult{path: p, reused: true}, nil
		}
		dl, err := downloadAsset(c.http, c.layout, assetURL, c.opts.MaxFileBytes, c.seen, !c.opts.Refresh)
		if err == nil {
			c.seenPaths[key] = struct{}{}
		}
		return dl, err
	}
	return downloadAsset(c.http, c.layout, assetURL, c.opts.MaxFileBytes, c.seen, !c.opts.Refresh)
}

// probePage reports whether a guessed page URL answers with HTML.
func (c *crawler) probePage(pageURL string) bool {
	resp, err := c.http.FetchURLWithLimit(pageURL, 8<<20)
	return err == nil && resp.OK && resp.IsHTML()
}

// fetchAsset downloads an asset referenced from pageURL, post-processes CSS,
// scripts and JSON, and returns the local file path. ok is false when the
// asset is out of scope or failed. Speculative URLs (guessed from string
// literals) fail silently.
func (c *crawler) fetchAsset(assetURL, pageURL string, speculative bool) (string, bool) {
	if !isHTTPURL(assetURL) {
		return "", false
	}
	if !shouldDownloadAsset(assetURL, c.domain, c.opts) {
		if !speculative {
			if u, err := url.Parse(assetURL); err == nil {
				c.skippedExternal[strings.ToLower(u.Hostname())]++
			}
		}
		return "", false
	}
	if _, bad := c.failed[assetURL]; bad {
		return "", false
	}
	if speculative {
		if _, tried := c.speculativeTried[assetURL]; tried {
			if _, ok := c.seen[assetURL]; !ok {
				return "", false
			}
		} else {
			if len(c.speculativeTried) >= maxSpeculative || c.pageSpeculative >= maxSpeculativePerPage {
				return "", false
			}
			c.speculativeTried[assetURL] = struct{}{}
			c.pageSpeculative++
		}
	}
	if err := c.opts.Pause.Wait(c.ctx); err != nil {
		return "", false
	}
	c.setCurrent(assetURL)
	dl, err := c.download(assetURL)
	if err != nil {
		if c.ctx.Err() != nil {
			return "", false
		}
		c.failed[assetURL] = struct{}{}
		if speculative {
			c.speculativeMiss++
		} else {
			reportAssetError(c.ctx, c.events, assetURL, err)
		}
		return "", false
	}
	// A guessed URL answered with an HTML page ("soft 404"): not the file
	// the literal named, so do not keep it or scan it.
	if speculative && !dl.reused && strings.Contains(strings.ToLower(dl.contentType), "text/html") && !isHTMLPath(urlPath(assetURL)) {
		_ = removeOutputFile(c.opts.OutputDir, dl.path)
		delete(c.seen, assetURL)
		delete(c.seenPaths, strings.ToLower(dl.path))
		c.failed[assetURL] = struct{}{}
		c.speculativeMiss++
		return "", false
	}
	c.totalBytes.Add(dl.size)
	if !dl.reused {
		c.assetsN.Add(1)
		c.events.OnAsset(assetURL, "asset")
	}
	c.postProcess(assetURL, pageURL, dl)
	return dl.path, true
}

// storeCaptured saves a response the browser already downloaded.
func (c *crawler) storeCaptured(r capturedResource, pageURL string) {
	if _, dup := c.seen[r.URL]; dup {
		return
	}
	if !shouldDownloadAsset(r.URL, c.domain, c.opts) {
		if u, err := url.Parse(r.URL); err == nil {
			c.skippedExternal[strings.ToLower(u.Hostname())]++
		}
		return
	}
	if c.opts.MaxFileBytes > 0 && int64(len(r.Body)) > c.opts.MaxFileBytes {
		return
	}
	p, err := c.layout.assetPath(r.URL)
	if err != nil {
		return
	}
	pathKey := strings.ToLower(p)
	if _, dup := c.seenPaths[pathKey]; dup {
		c.seen[r.URL] = struct{}{}
		return
	}
	c.seenPaths[pathKey] = struct{}{}
	if err := writeOutputFile(c.opts.OutputDir, p, r.Body); err != nil {
		reportAssetError(c.ctx, c.events, r.URL, err)
		return
	}
	c.seen[r.URL] = struct{}{}
	c.totalBytes.Add(int64(len(r.Body)))
	c.assetsN.Add(1)
	c.events.OnAsset(r.URL, "asset")
	c.postProcess(r.URL, pageURL, downloadResult{path: p, size: int64(len(r.Body)), body: r.Body, contentType: r.MimeType})
}

// postProcess rewrites CSS and mines scripts / JSON for more references.
// Files reused from an earlier run are read back: scripts and JSON for
// scanning, CSS to finish a rewrite that run may not have completed
// (references already pointing at local files are kept).
func (c *crawler) postProcess(assetURL, pageURL string, dl downloadResult) {
	kind := assetKind(dl.path, dl.contentType)
	if kind == "" {
		return
	}
	if kind == "css" {
		if dl.reused {
			return // already handled in this run
		}
		if dl.body == nil {
			body, err := readOutputFile(c.opts.OutputDir, dl.path)
			if err != nil {
				return
			}
			_, _ = c.rewriteCSS(dl.path, string(body), assetURL, true)
			return
		}
		_, _ = c.rewriteCSS(dl.path, string(dl.body), assetURL, false)
		return
	}
	if _, done := c.scanned[assetURL]; !done {
		c.scanned[assetURL] = struct{}{}
		body := dl.body
		if body == nil {
			body, _ = readOutputFile(c.opts.OutputDir, dl.path)
		}
		if looksLikeHTML(body) {
			return // an error page served under a .js/.json name
		}
		switch kind {
		case "js":
			c.scanScript(string(body), assetURL)
		case "json":
			c.scanJSON(body, assetURL, pageURL)
		}
	}
	if kind == "js" {
		c.resolveLiterals(c.jsLiterals[assetURL], pageURL, assetURL)
	}
}

// assetKind classifies a file for post-processing.
func assetKind(p, contentType string) string {
	ct := strings.ToLower(contentType)
	switch strings.ToLower(filepath.Ext(p)) {
	case ".css":
		return "css"
	case ".js", ".mjs":
		return "js"
	case ".json":
		return "json"
	}
	switch {
	case strings.Contains(ct, "text/css"):
		return "css"
	case strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript"):
		return "js"
	case strings.Contains(ct, "json"):
		return "json"
	}
	return ""
}

// scanScript follows module references of a script and remembers its
// document-relative literals.
func (c *crawler) scanScript(src, scriptURL string) {
	for _, ref := range ScanJS(src) {
		switch ref.Kind {
		case JSRefModule:
			c.needsServer = true
			if abs, err := absoluteURL(scriptURL, ref.URL); err == nil {
				c.fetchAsset(abs, scriptURL, true)
			}
		case JSRefDocument:
			c.jsLiterals[scriptURL] = append(c.jsLiterals[scriptURL], ref.URL)
		}
	}
}

// scanJSON follows path-like strings in a JSON file. Data files name
// siblings ("win95.css" next to themes.json) as well as site paths
// ("icons/a.png"), so both bases are tried.
func (c *crawler) scanJSON(data []byte, jsonURL, pageURL string) {
	c.needsServer = true // fetched JSON implies fetch()
	lits := ScanJSON(data)
	c.resolveLiterals(lits, jsonURL, jsonURL)
	if pageURL != "" && pageURL != jsonURL {
		c.resolveLiterals(lits, pageURL, jsonURL)
	}
}

// guessGiveUp stops guessing from one script or JSON file (for one page
// folder) after this many guessed URLs turned out not to exist (and none did).
const guessGiveUp = 20

type guessStat struct{ tries, hits int }

// resolveLiterals turns document-relative literals into speculative
// downloads (files) or speculative pages (.html). source names the script
// or JSON file the literals came from.
func (c *crawler) resolveLiterals(lits []string, baseURL, source string) {
	// Counted per source and base folder: the same data file can name
	// files that exist relative to one page folder and not another.
	key := source + "|" + path.Dir(urlPath(baseURL)+"x")
	st := c.guesses[key]
	if st == nil {
		st = &guessStat{}
		c.guesses[key] = st
	}
	for _, lit := range lits {
		if st.hits == 0 && st.tries >= guessGiveUp {
			return // this file's strings are evidently not paths here
		}
		abs, err := absoluteURL(baseURL, lit)
		if err != nil || !isHTTPURL(abs) {
			continue
		}
		u, err := url.Parse(abs)
		if err != nil {
			continue
		}
		u.Fragment = ""
		abs = u.String()
		if isHTMLPath(u.Path) {
			if shouldFollowPage(abs, c.domain, c.opts) {
				c.discoveredPages = append(c.discoveredPages, abs)
			}
			continue
		}
		if !sameScopeHost(u.Hostname(), c.domain, c.opts) {
			continue // never guess CDN files
		}
		missesBefore := c.speculativeMiss
		if _, ok := c.fetchAsset(abs, baseURL, true); ok {
			st.tries++
			st.hits++
		} else if c.speculativeMiss > missesBefore {
			st.tries++
		}
	}
}

// downloadPage fetches a single page, downloads its assets (rewriting the
// HTML in place), and classifies outbound links into pages to follow and
// attachments. A link that turns out not to be HTML is saved as a file.
func (c *crawler) downloadPage(pageURL string) (links, attachments []string, err error) {
	if err := c.opts.Pause.Wait(c.ctx); err != nil {
		return nil, nil, err
	}
	// A link that turns out not to be HTML is saved where links to it are
	// rewritten, i.e. the asset path (unless that file is already there
	// from this run, e.g. a rewritten stylesheet).
	_, alreadySaved := c.seen[pageURL]
	assetPath, assetPathErr := c.layout.assetPath(pageURL)
	target := ""
	if !alreadySaved && assetPathErr == nil {
		target = assetPath
	}
	resp, err := c.fetchPage(pageURL, target)
	if err != nil {
		return nil, nil, err
	}
	if resp.Streamed || !resp.IsHTML() {
		if alreadySaved {
			return nil, nil, nil
		}
		if assetPathErr != nil {
			return nil, nil, fmt.Errorf("asset path: %w", assetPathErr)
		}
		return nil, nil, c.saveNonHTML(pageURL, assetPath, resp)
	}
	return c.savePage(pageURL, resp)
}

// fetchPage fetches a page and checks status and size. In HTTP mode only
// HTML is read into memory (capped); anything else is streamed straight to
// streamTarget, so a large extension-less download does not end up in RAM.
func (c *crawler) fetchPage(pageURL, streamTarget string) (*FetchResult, error) {
	var resp *FetchResult
	var err error
	if f, ok := c.fetcher.(*Fetcher); ok {
		resp, err = f.FetchPageOrFile(pageURL, c.opts.MaxFileBytes, c.opts.OutputDir, streamTarget)
	} else {
		resp, err = c.fetcher.FetchPage(pageURL)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if c.opts.MaxFileBytes > 0 && int64(len(resp.Body)) > c.opts.MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	if !resp.OK {
		return nil, &HTTPStatusError{Status: resp.Status}
	}
	return resp, nil
}

// saveNonHTML keeps a "page" that turned out to be a file (it is already on
// disk when it was streamed).
func (c *crawler) saveNonHTML(pageURL, assetPath string, resp *FetchResult) error {
	if !resp.Streamed {
		if err := writeOutputFile(c.opts.OutputDir, assetPath, resp.Body); err != nil {
			return fmt.Errorf("write file: %w", &localWriteError{err: err})
		}
	}
	c.seen[pageURL] = struct{}{}
	c.totalBytes.Add(resp.Size)
	return nil
}

// savePage rewrites an HTML page to the local mirror (downloading its
// assets on the way), saves it and returns its links and attachments.
func (c *crawler) savePage(pageURL string, resp *FetchResult) (links, attachments []string, err error) {
	doc, refs, parseErr := RewriteAssets(string(resp.Body))
	if parseErr != nil {
		return nil, nil, fmt.Errorf("parse html: %w", parseErr)
	}
	c.totalBytes.Add(int64(len(resp.Body)))

	// The page is saved under the URL it was linked as, but its relative
	// links resolve against where the server sent us (/docs -> /docs/).
	baseURL := pageURL
	if isHTTPURL(resp.FinalURL) {
		baseURL = resp.FinalURL
	}
	pagePath, err := c.layout.pagePath(pageURL)
	if err != nil {
		return nil, nil, fmt.Errorf("page path: %w", err)
	}
	pageDir := filepath.Dir(pagePath)

	if err := c.fetchPageAssets(doc, refs, resp.Rendered, baseURL, pageDir); err != nil {
		return nil, nil, err
	}
	c.processScripts(doc, baseURL, pageDir)

	// Rewrite same-scope navigation to the local mirror and classify what the
	// crawler should visit/download next.
	links, attachments = rewriteLocalNavigation(doc, baseURL, pageDir, c.layout, c.domain, c.opts)
	if resp.Rendered != nil {
		links = c.addRenderedLinks(links, resp.Rendered.Links)
	}
	rewriteMetaRefresh(doc, baseURL, pageDir, c.layout)

	htmlOut, err := doc.Html()
	if err != nil {
		return links, attachments, fmt.Errorf("render html: %w", err)
	}
	if err := writeOutputFile(c.opts.OutputDir, pagePath, []byte(htmlOut)); err != nil {
		return links, attachments, fmt.Errorf("write html: %w", &localWriteError{err: err})
	}
	return links, attachments, nil
}

// fetchPageAssets downloads the assets a page references and points the
// references at the local copies. In browser mode what the browser loaded
// is saved first, so the references find the files already in place.
func (c *crawler) fetchPageAssets(doc *goquery.Document, refs []AssetRef, rendered *renderResult, baseURL, pageDir string) error {
	if rendered != nil {
		for _, r := range rendered.Resources {
			if err := c.opts.Pause.Wait(c.ctx); err != nil {
				return err
			}
			c.storeCaptured(r, baseURL)
		}
	}
	for _, ref := range refs {
		if err := c.opts.Pause.Wait(c.ctx); err != nil {
			return err
		}
		assetURL, err := absoluteURL(baseURL, ref.URL)
		if err != nil {
			continue
		}
		if isEmbeddedDocumentRef(ref) && !sameScopeAssetURL(assetURL, c.domain, c.opts) {
			continue
		}
		localPath, ok := c.fetchAsset(assetURL, baseURL, false)
		if !ok {
			continue
		}
		if rel, ok := localRef(pageDir, localPath, assetURL); ok {
			RewriteAssetURL(doc, ref.Tag, ref.Attr, ref.URL, rel)
		}
	}
	return nil
}

// addRenderedLinks adds the page links that only exist in the rendered DOM
// (built by scripts).
func (c *crawler) addRenderedLinks(links, rendered []string) []string {
	seen := make(map[string]struct{}, len(links))
	for _, l := range links {
		seen[l] = struct{}{}
	}
	for _, l := range rendered {
		u, err := url.Parse(l)
		if err != nil {
			continue
		}
		u.Fragment = ""
		l = u.String()
		ext := strings.ToLower(path.Ext(u.Path))
		htmlish := ext == "" || ext == ".html" || ext == ".htm" || u.RawQuery != ""
		if !htmlish || !isHTTPURL(l) || !shouldFollowPage(l, c.domain, c.opts) {
			continue
		}
		if _, dup := seen[l]; !dup {
			seen[l] = struct{}{}
			links = append(links, l)
		}
	}
	return links
}

// processScripts handles import maps and inline scripts of a page.
func (c *crawler) processScripts(doc *goquery.Document, pageURL, pageDir string) {
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		typ := strings.ToLower(strings.TrimSpace(s.AttrOr("type", "")))
		if typ == "module" {
			c.needsServer = true
		}
		if _, hasSrc := s.Attr("src"); hasSrc {
			// External scripts are post-processed when downloaded; their
			// literals still resolve against this page.
			if src, ok := s.Attr("src"); ok {
				if abs, err := absoluteURL(pageURL, src); err == nil {
					if lits, ok := c.jsLiterals[abs]; ok {
						c.resolveLiterals(lits, pageURL, abs)
					}
				}
			}
			return
		}
		body := s.Text()
		switch typ {
		case "importmap":
			c.needsServer = true
			replace := map[string]string{}
			for _, target := range ImportMapTargets(body) {
				abs, err := absoluteURL(pageURL, target)
				if err != nil {
					continue
				}
				localPath, ok := c.fetchAsset(abs, pageURL, false)
				if !ok {
					continue
				}
				if rel, ok := localRef(pageDir, localPath, abs); ok {
					if !strings.HasPrefix(rel, ".") {
						rel = "./" + rel
					}
					replace[target] = rel
				}
			}
			if newBody := RewriteImportMap(body, replace); newBody != body {
				setRawText(s, newBody)
			}
		case "", "text/javascript", "application/javascript", "module":
			for _, ref := range ScanJS(body) {
				abs, err := absoluteURL(pageURL, ref.URL)
				if err != nil {
					continue
				}
				if ref.Kind == JSRefModule {
					c.needsServer = true
					c.fetchAsset(abs, pageURL, true)
				} else {
					c.resolveLiterals([]string{ref.URL}, pageURL, pageURL+"#inline")
				}
			}
		}
	})
}

// localRef returns the relative URL from dir to the saved file, keeping the
// original query for static files that are saved without it ("app.js?v=7").
// Keeping it preserves module identity: a module imported once as
// "app.js?v=7" and once as "app.js" would otherwise run twice.
func localRef(dir, localPath, assetURL string) (string, bool) {
	rel, err := relHref(dir, localPath)
	if err != nil {
		return "", false
	}
	if u, err := url.Parse(assetURL); err == nil && u.RawQuery != "" && !isDynamicAssetPath(strings.TrimPrefix(u.Path, "/")) {
		rel += "?" + u.RawQuery
	}
	return rel, true
}

func isEmbeddedDocumentRef(ref AssetRef) bool {
	switch ref.Tag {
	case "iframe", "embed", "object":
		return true
	default:
		return false
	}
}

func sameScopeAssetURL(absURL, baseDomain string, opts Options) bool {
	parsed, err := url.Parse(absURL)
	if err != nil {
		return false
	}
	return sameScopeHost(parsed.Hostname(), baseDomain, opts)
}

// downloadResult bundles the on-disk path, size and content-type of a
// downloaded asset. body is populated for CSS, scripts and JSON.
type downloadResult struct {
	path        string
	size        int64
	body        []byte
	contentType string
	reused      bool // already counted in this run; still rewrite references
}

// downloadAsset streams an asset to disk. Files that already exist
// (complete, thanks to atomic writes) are reused when reuseExisting is set.
// The URL is recorded in seen (if non-nil) to dedupe across pages.
func downloadAsset(fetcher *Fetcher, l layout, assetURL string, maxBytes int64, seen map[string]struct{}, reuseExisting bool) (downloadResult, error) {
	baseDir := l.dir
	fullPath, pathErr := l.assetPath(assetURL)
	if pathErr != nil {
		return downloadResult{}, pathErr
	}
	if seen != nil {
		if _, dup := seen[assetURL]; dup {
			return downloadResult{path: fullPath, reused: true}, nil
		}
	}
	if reuseExisting {
		if info, err := statOutputFile(baseDir, fullPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			if seen != nil {
				seen[assetURL] = struct{}{}
			}
			return downloadResult{path: fullPath}, nil
		}
	}
	resp, err := fetcher.FetchToFile(assetURL, maxBytes, baseDir, fullPath)
	if err != nil {
		return downloadResult{}, err
	}
	if !resp.OK {
		return downloadResult{}, &HTTPStatusError{Status: resp.Status}
	}
	dl := downloadResult{path: fullPath, size: resp.Size, contentType: resp.ContentType}
	if assetKind(fullPath, resp.ContentType) != "" {
		if body, err := readOutputFile(baseDir, fullPath); err == nil {
			dl.body = body
		}
	}
	if seen != nil {
		seen[assetURL] = struct{}{}
	}
	return dl, nil
}

// reportAssetError forwards a failed asset/attachment download to the error
// log unless the run itself was cancelled.
func reportAssetError(ctx context.Context, events Events, assetURL string, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	events.OnError(assetURL, fmt.Errorf("asset: %w", err))
}

func absoluteURL(base, ref string) (string, error) {
	baseParsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	refParsed, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	return baseParsed.ResolveReference(refParsed).String(), nil
}

// ErrInvalidURL is returned when the user passes a malformed URL.
var ErrInvalidURL = errors.New("invalid url")

// setRawText replaces an element's content with one raw text node.
// goquery's SetText would HTML-escape it, which breaks <script> contents.
func setRawText(s *goquery.Selection, text string) {
	for _, n := range s.Nodes {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			n.RemoveChild(c)
			c = next
		}
		n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	}
}

// snapshot builds a progress event from the live counters.
func (c *crawler) snapshot(status string) PageEvent {
	ev := PageEvent{
		Status:      status,
		Pages:       int(c.pagesDone.Load()),
		Assets:      int(c.assetsN.Load()),
		Attachments: int(c.attachments.Load()),
		Bytes:       c.totalBytes.Load(),
		Queued:      int(c.queueLen.Load()),
		Transferred: c.http.Transferred(),
	}
	if cur := c.current.Load(); cur != nil {
		ev.Current = *cur
	}
	return ev
}

// emitPage serialises OnPage calls from the crawl and the ticker.
func (c *crawler) emitPage(ev PageEvent) {
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	c.events.OnPage(ev)
}

func (c *crawler) setCurrent(u string) { c.current.Store(&u) }

func urlPath(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return raw
}

// looksLikeHTML reports whether a body starts like an HTML document.
func looksLikeHTML(body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	head = strings.TrimPrefix(head, "\ufeff")
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.HasPrefix(head, "<head")
}
