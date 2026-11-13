package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// SkipExisting reuses complete asset files already in OutputDir
	// (resume). Refresh overrides it: every file is downloaded again and
	// overwritten (updating an existing copy).
	SkipExisting bool
	Refresh      bool
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
// goroutine running Download.
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
	speculativeMiss int
	needsServer     bool
}

// Download runs a recursive download described by opts and emits progress
// events through events. It returns once every reachable page up to
// MaxDepth has been processed, one of the limits has been reached, or the
// run was cancelled. A failure to save even the start page is reported in
// Summary.Failed (and OnComplete), not as an error.
func Download(opts Options, events Events) (Summary, error) {
	started := time.Now()
	if events.OnPage == nil {
		events.OnPage = func(PageEvent) {}
	}
	if events.OnAsset == nil {
		events.OnAsset = func(string, string) {}
	}
	if events.OnError == nil {
		events.OnError = func(string, error) {}
	}
	if events.OnComplete == nil {
		events.OnComplete = func(Summary) {}
	}
	if opts.MaxDepth < 1 {
		opts.MaxDepth = 1
	}
	if opts.Mode == "" {
		opts.Mode = ModeHTTP
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Retries == 0 {
		opts.Retries = 2
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.PageDelay == 0 {
		opts.PageDelay = 250 * time.Millisecond
	}

	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return Summary{}, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return Summary{}, fmt.Errorf("%w: expected an absolute HTTP(S) URL", ErrInvalidURL)
	}
	startFile, err := FilePathFor(opts.OutputDir, opts.URL)
	if err != nil {
		return Summary{}, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	_, statErr := os.Stat(opts.OutputDir)
	createdDir := os.IsNotExist(statErr)
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Summary{}, fmt.Errorf("create output dir: %w", err)
	}

	c := &crawler{
		opts:             opts,
		domain:           parsed.Hostname(),
		events:           events,
		seen:             make(map[string]struct{}),
		seenPaths:        make(map[string]struct{}),
		jsLiterals:       make(map[string][]string),
		scanned:          make(map[string]struct{}),
		speculativeTried: make(map[string]struct{}),
		failed:           make(map[string]struct{}),
		guesses:          make(map[string]*guessStat),
		skippedExternal:  make(map[string]int),
	}
	c.http = NewFetcherWithContext(opts.Context, opts.Retries)
	c.http.SetCookie(opts.Cookie, opts.URL)
	defer c.http.Cancel()
	c.ctx = c.http.ctx
	c.fetcher = c.http

	// Open the error log (one line per failure). Best-effort.
	var errLog *os.File
	if root, err := os.OpenRoot(opts.OutputDir); err == nil {
		if f, ferr := root.OpenFile("download-errors.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
			errLog = f
		}
		root.Close()
	}
	uiOnError := events.OnError
	c.events.OnError = func(pageURL string, err error) {
		c.reportErrors = append(c.reportErrors, map[string]string{"url": pageURL, "error": err.Error()})
		uiOnError(pageURL, err)
		if errLog != nil {
			line := time.Now().Format(time.RFC3339) + " " + pageURL + " " + err.Error() + "\n"
			_, _ = errLog.WriteString(line)
		}
	}

	summary := Summary{OutputDir: opts.OutputDir, ReportPath: filepath.Join(opts.OutputDir, "download-report.json")}
	finish := func() (Summary, error) {
		summary.Bytes = c.totalBytes.Load()
		summary.Assets = int(c.assetsN.Load())
		summary.Pages = int(c.pagesDone.Load())
		summary.Attachments = int(c.attachments.Load())
		summary.Errors = len(c.reportErrors)
		summary.SkippedExternal, summary.SkippedExternalHosts = c.skippedTotals()
		summary.NeedsServer = c.needsServer
		summary.Elapsed = time.Since(started)
		if summary.Pages > 0 {
			if _, err := os.Stat(startFile); err == nil {
				summary.StartFile = startFile
			}
		}
		if errLog != nil {
			_ = errLog.Close()
		}
		if summary.Failed && createdDir && onlyRunMetadata(opts.OutputDir) {
			// Nothing was saved: do not leave an empty folder behind.
			_ = os.RemoveAll(opts.OutputDir)
			summary.ReportPath = ""
		} else {
			writeReport(summary.ReportPath, opts, summary, c.reportErrors, c.speculativeMiss)
		}
		c.events.OnComplete(summary)
		return summary, nil
	}

	if opts.Mode == ModeBrowser {
		bf, bfErr := newBrowserFetcher(c.http, c.ctx, opts.Engine, opts.MaxFileBytes)
		if bfErr != nil {
			if c.ctx.Err() != nil {
				summary.Cancelled = true
				return finish()
			}
			c.events.OnError(opts.URL, bfErr)
			summary.Failed = true
			summary.FailKind = "browser"
			summary.FailDetail = bfErr.Error()
			return finish()
		}
		c.fetcher = bf
		summary.Engine = bf.EngineName()
		defer bf.Close()
	}

	type queueItem struct {
		URL         string
		Depth       int
		Speculative bool
	}
	queue := []queueItem{{URL: opts.URL, Depth: 0}}
	queued := map[string]struct{}{opts.URL: {}}
	visited := make(map[string]struct{})
	visitedFiles := make(map[string]struct{}) // "/" and "/index.html" are one page
	attachmentsSeen := make(map[string]struct{})

	emit := func(item queueItem, status string) {
		c.queueLen.Store(int64(len(queue)))
		ev := c.snapshot(status)
		ev.URL, ev.Depth = item.URL, item.Depth
		c.emitPage(ev)
	}
	// The ticker reports bytes and the current file while the crawl is
	// busy inside one page or one large file.
	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case <-c.ctx.Done():
				return
			case <-t.C:
				c.emitPage(c.snapshot("progress"))
			}
		}
	}()
	defer func() {
		close(stopTicker)
		<-tickerDone
	}()

loop:
	for {
		if err := opts.Pause.Wait(c.ctx); err != nil {
			summary.Cancelled = true
			break
		}
		if c.ctx.Err() != nil {
			summary.Cancelled = true
			break
		}
		if opts.MaxPages > 0 && int(c.pagesDone.Load()) >= opts.MaxPages {
			break
		}
		if opts.MaxTotalBytes > 0 && c.totalBytes.Load() >= opts.MaxTotalBytes {
			break
		}
		if len(queue) == 0 {
			break
		}

		item := queue[0]
		queue = queue[1:]
		if _, ok := visited[item.URL]; ok {
			continue
		}
		if item.Depth >= opts.MaxDepth {
			continue
		}
		visited[item.URL] = struct{}{}
		if p, err := FilePathFor(opts.OutputDir, item.URL); err == nil {
			key := strings.ToLower(p)
			if _, dup := visitedFiles[key]; dup {
				continue
			}
			visitedFiles[key] = struct{}{}
		}
		// Pages guessed from script literals are probed over plain HTTP
		// first: rendering a 404 in the browser would cost seconds each.
		if item.Speculative && opts.Mode == ModeBrowser && !c.probePage(item.URL) {
			c.speculativeMiss++
			continue
		}
		emit(item, "downloading")

		c.discoveredPages = nil
		c.pageSpeculative = 0
		links, attachments, err := c.downloadPage(item.URL)
		if err != nil {
			if errors.Is(err, context.Canceled) || c.ctx.Err() != nil {
				summary.Cancelled = true
				break
			}
			if item.Speculative {
				c.speculativeMiss++
				continue
			}
			c.events.OnError(item.URL, err)
			if item.Depth == 0 && c.pagesDone.Load() == 0 {
				summary.Failed = true
				summary.FailKind, summary.FailStatus = classifyError(err)
				summary.FailDetail = err.Error()
			}
			emit(item, "error")
			continue
		}
		c.pagesDone.Add(1)

		// Queue the links first, so progress knows what is left while the
		// attachments (possibly large) download.
		if item.Depth+1 < opts.MaxDepth {
			enqueue := func(link string, speculative bool) {
				if _, ok := queued[link]; ok {
					return
				}
				queued[link] = struct{}{}
				queue = append(queue, queueItem{URL: link, Depth: item.Depth + 1, Speculative: speculative})
			}
			for _, link := range links {
				enqueue(link, false)
			}
			for _, link := range c.discoveredPages {
				enqueue(link, true)
			}
		}
		c.queueLen.Store(int64(len(queue)))

		if opts.DownloadAll {
			for _, attURL := range attachments {
				if err := opts.Pause.Wait(c.ctx); err != nil {
					summary.Cancelled = true
					break loop
				}
				if _, ok := attachmentsSeen[attURL]; ok {
					continue
				}
				attachmentsSeen[attURL] = struct{}{}
				c.setCurrent(attURL)
				dl, err := c.download(attURL)
				if err != nil {
					reportAssetError(c.ctx, c.events, attURL, err)
				} else if !dl.reused {
					c.attachments.Add(1)
					c.totalBytes.Add(dl.size)
				}
				c.events.OnAsset(attURL, "attachment")
				if opts.MaxTotalBytes > 0 && c.totalBytes.Load() >= opts.MaxTotalBytes {
					break
				}
			}
		}

		emit(item, "done")
		log.Printf("[PROGRESS] pages=%d assets=%d attachments=%d bytes=%d queued=%d",
			c.pagesDone.Load(), c.assetsN.Load(), c.attachments.Load(), c.totalBytes.Load(), len(queue))

		if opts.PageDelay > 0 && len(queue) > 0 {
			select {
			case <-c.ctx.Done():
				summary.Cancelled = true
				break loop
			case <-time.After(opts.PageDelay):
			}
		}
	}
	return finish()
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

func (c *crawler) skippedTotals() (int, []string) {
	total := 0
	hosts := make([]string, 0, len(c.skippedExternal))
	for h, n := range c.skippedExternal {
		total += n
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return c.skippedExternal[hosts[i]] > c.skippedExternal[hosts[j]] ||
			(c.skippedExternal[hosts[i]] == c.skippedExternal[hosts[j]] && hosts[i] < hosts[j])
	})
	return total, hosts
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
		"speculativeMisses":    speculativeMiss,
		"elapsedSeconds":       summary.Elapsed.Seconds(),
		"finishedAt":           time.Now().Format(time.RFC3339),
		"errors":               errs,
	}
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = writeOutputFile(opts.OutputDir, path, data)
	}
}

// shouldFollow reports whether the crawler may follow an HTML/link URL. It
// never uses IncludeExternalAssets: that flag is intentionally limited to
// assets (CDN fonts/images/scripts), not outbound page crawling.
func shouldFollow(absURL, baseDomain string, opts Options) bool {
	return shouldFollowPage(absURL, baseDomain, opts)
}

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
	if p, err := AssetPathFor(c.opts.OutputDir, assetURL); err == nil {
		key := strings.ToLower(p)
		if _, dup := c.seenPaths[key]; dup {
			c.seen[assetURL] = struct{}{}
			return downloadResult{path: p, reused: true}, nil
		}
		dl, err := downloadAsset(c.http, c.opts.OutputDir, assetURL, c.opts.MaxFileBytes, c.seen, !c.opts.Refresh)
		if err == nil {
			c.seenPaths[key] = struct{}{}
		}
		return dl, err
	}
	return downloadAsset(c.http, c.opts.OutputDir, assetURL, c.opts.MaxFileBytes, c.seen, !c.opts.Refresh)
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
	p, err := AssetPathFor(c.opts.OutputDir, r.URL)
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
// Files reused from an earlier run are read back for scanning (their CSS
// was already rewritten then).
func (c *crawler) postProcess(assetURL, pageURL string, dl downloadResult) {
	kind := assetKind(dl.path, dl.contentType)
	if kind == "" {
		return
	}
	if kind == "css" {
		if dl.reused || dl.body == nil {
			return
		}
		_, _ = c.rewriteCSS(dl.path, string(dl.body), assetURL)
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
// attachments.
func (c *crawler) downloadPage(pageURL string) (links, attachments []string, err error) {
	opts := c.opts
	if err := opts.Pause.Wait(c.ctx); err != nil {
		return nil, nil, err
	}
	var resp *FetchResult
	var fetchErr error
	if f, ok := c.fetcher.(*Fetcher); ok {
		resp, fetchErr = f.FetchURLWithLimit(pageURL, opts.MaxFileBytes)
	} else {
		resp, fetchErr = c.fetcher.FetchPage(pageURL)
	}
	if fetchErr != nil {
		return nil, nil, fmt.Errorf("fetch: %w", fetchErr)
	}
	if opts.MaxFileBytes > 0 && int64(len(resp.Body)) > opts.MaxFileBytes {
		return nil, nil, ErrFileTooLarge
	}
	if !resp.OK {
		return nil, nil, &HTTPStatusError{Status: resp.Status}
	}
	if !resp.IsHTML() {
		// Not an HTML page: save the body we already have (no second
		// request) where links to it are rewritten, i.e. the asset path.
		if _, dup := c.seen[pageURL]; dup {
			return nil, nil, nil
		}
		assetPath, pErr := AssetPathFor(opts.OutputDir, pageURL)
		if pErr != nil {
			return nil, nil, fmt.Errorf("asset path: %w", pErr)
		}
		if err := writeOutputFile(opts.OutputDir, assetPath, resp.Body); err != nil {
			return nil, nil, fmt.Errorf("write file: %w", err)
		}
		c.seen[pageURL] = struct{}{}
		c.totalBytes.Add(int64(len(resp.Body)))
		return nil, nil, nil
	}

	doc, refs, parseErr := RewriteAssets(string(resp.Body))
	if parseErr != nil {
		return nil, nil, fmt.Errorf("parse html: %w", parseErr)
	}
	c.totalBytes.Add(int64(len(resp.Body)))

	pagePath, fpErr := FilePathFor(opts.OutputDir, pageURL)
	if fpErr != nil {
		return nil, nil, fmt.Errorf("page path: %w", fpErr)
	}
	pageDir := filepath.Dir(pagePath)

	// Browser mode: save what the browser loaded first, so the references
	// below find the files already in place.
	if resp.Rendered != nil {
		for _, r := range resp.Rendered.Resources {
			if err := opts.Pause.Wait(c.ctx); err != nil {
				return nil, nil, err
			}
			c.storeCaptured(r, pageURL)
		}
	}

	for _, ref := range refs {
		if err := opts.Pause.Wait(c.ctx); err != nil {
			return links, attachments, err
		}
		assetURL, absErr := absoluteURL(pageURL, ref.URL)
		if absErr != nil {
			continue
		}
		if isEmbeddedDocumentRef(ref) && !sameScopeAssetURL(assetURL, c.domain, opts) {
			continue
		}
		localPath, ok := c.fetchAsset(assetURL, pageURL, false)
		if !ok {
			continue
		}
		if rel, ok := localRef(pageDir, localPath, assetURL); ok {
			RewriteAssetURL(doc, ref.Tag, ref.Attr, ref.URL, rel)
		}
	}

	c.processScripts(doc, pageURL, pageDir)

	// Rewrite same-scope navigation to the local mirror and classify what the
	// crawler should visit/download next.
	links, attachments = RewriteLocalNavigation(doc, pageURL, opts.OutputDir, c.domain, opts)

	// Links that only exist in the rendered DOM (built by scripts).
	if resp.Rendered != nil {
		seen := make(map[string]struct{}, len(links))
		for _, l := range links {
			seen[l] = struct{}{}
		}
		for _, l := range resp.Rendered.Links {
			if u, err := url.Parse(l); err == nil {
				u.Fragment = ""
				l = u.String()
				ext := strings.ToLower(path.Ext(u.Path))
				htmlish := ext == "" || ext == ".html" || ext == ".htm" || u.RawQuery != ""
				if !htmlish || !isHTTPURL(l) || !shouldFollowPage(l, c.domain, opts) {
					continue
				}
				if _, dup := seen[l]; !dup {
					seen[l] = struct{}{}
					links = append(links, l)
				}
			}
		}
	}

	RewriteMetaRefresh(doc, pageURL, opts.OutputDir)

	htmlOut, err := doc.Html()
	if err != nil {
		return links, attachments, fmt.Errorf("render html: %w", err)
	}
	if err := writeOutputFile(opts.OutputDir, pagePath, []byte(htmlOut)); err != nil {
		return links, attachments, fmt.Errorf("write html: %w", err)
	}
	return links, attachments, nil
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
	rel, err := filepath.Rel(dir, localPath)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
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

// isCSS is true when the file looks like a CSS stylesheet by extension or
// Content-Type header.
func isCSS(path, contentType string) bool {
	return assetKind(path, contentType) == "css"
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

// downloadAssetFileWithLimit streams an asset to disk with an optional
// per-file size cap, reusing complete files already on disk.
func downloadAssetFileWithLimit(fetcher *Fetcher, baseDir, assetURL string, maxBytes int64, seen map[string]struct{}) (downloadResult, error) {
	return downloadAsset(fetcher, baseDir, assetURL, maxBytes, seen, true)
}

// downloadAsset streams an asset to disk. Files that already exist
// (complete, thanks to atomic writes) are reused when reuseExisting is set.
// The URL is recorded in seen (if non-nil) to dedupe across pages.
func downloadAsset(fetcher *Fetcher, baseDir, assetURL string, maxBytes int64, seen map[string]struct{}, reuseExisting bool) (downloadResult, error) {
	fullPath, pathErr := AssetPathFor(baseDir, assetURL)
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
