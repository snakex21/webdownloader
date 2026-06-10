package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
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
// uses the standard HTTP client. "browser" mode renders every page through
// a headless Chromium (useful for SPAs and other JS-heavy sites).
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
	MaxDepth  int
	// Mode selects the fetcher: "http" (default) or "browser".
	Mode Mode
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
	Retries      int
	Cookie       string
	SkipExisting bool
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
}

// Events receives progress notifications from a running download. All
// callbacks are optional; they are invoked on the same goroutine that runs
// Download, so implementations must be fast (or push to a channel).
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
}

// pageFetcher is the contract Download uses to grab one page. The default
// HTTP implementation lives in this file; the "browser" implementation is
// in browser.go.
type pageFetcher interface {
	FetchPage(url string) (*FetchResult, error)
	Close() error
}

// Download runs a recursive download described by opts and emits progress
// events through events. It returns once every reachable page up to
// MaxDepth has been processed, one of the limits has been reached, or the
// user calls fetcher.Cancel().
func Download(opts Options, events Events) (Summary, error) {
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

	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return Summary{}, fmt.Errorf("invalid url: %w", err)
	}
	domain := parsed.Hostname()

	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return Summary{}, fmt.Errorf("create output dir: %w", err)
	}

	httpFetcher := NewFetcherWithContext(opts.Context, opts.Retries)
	httpFetcher.SetCookie(opts.Cookie)
	defer httpFetcher.Cancel()

	var fetcher pageFetcher = httpFetcher
	if opts.Mode == ModeBrowser {
		bf, bfErr := newBrowserFetcher(httpFetcher, httpFetcher.ctx)
		if bfErr != nil {
			events.OnError(opts.URL, bfErr)
			return Summary{}, bfErr
		}
		fetcher = bf
		defer bf.Close()
	}

	// Open the error log (one line per failure). Best-effort: a failure
	// to open the log is not fatal.
	var errLog *os.File
	if path, err := filepath.Abs(filepath.Join(opts.OutputDir, "download-errors.log")); err == nil {
		if f, ferr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
			errLog = f
			defer f.Close()
		}
	}
	logError := func(pageURL string, err error) {
		events.OnError(pageURL, err)
		if errLog != nil {
			line := time.Now().Format(time.RFC3339) + " " + pageURL + " " + err.Error() + "\n"
			_, _ = errLog.WriteString(line)
		}
	}
	var reportErrors []map[string]string
	logReportError := func(pageURL string, err error) {
		reportErrors = append(reportErrors, map[string]string{"url": pageURL, "error": err.Error()})
		logError(pageURL, err)
	}

	type queueItem struct {
		URL   string
		Depth int
	}
	queue := []queueItem{{URL: opts.URL, Depth: 0}}
	visited := make(map[string]struct{})
	attachmentsSeen := make(map[string]struct{})

	var (
		pageCount       int
		assetCount      int
		attachmentCount int
		totalBytes      atomic.Int64
		cancelled       bool
	)
	// Track every URL we've ever asked the OS to write so we don't fetch
	// the same asset twice (cheap deduplication).
	downloadedAssets := make(map[string]struct{})

	emit := func(ev PageEvent) { events.OnPage(ev) }
	ctx := httpFetcher.ctx

loop:
	for {
		if err := opts.Pause.Wait(ctx); err != nil {
			cancelled = true
			break loop
		}
		select {
		case <-ctx.Done():
			cancelled = true
			break loop
		default:
		}

		if opts.MaxPages > 0 && pageCount >= opts.MaxPages {
			break
		}
		if opts.MaxTotalBytes > 0 && totalBytes.Load() >= opts.MaxTotalBytes {
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
		if item.Depth > opts.MaxDepth {
			continue
		}
		visited[item.URL] = struct{}{}

		emit(PageEvent{
			URL:         item.URL,
			Status:      "downloading",
			Depth:       item.Depth,
			Pages:       pageCount,
			Assets:      assetCount,
			Attachments: attachmentCount,
			Bytes:       totalBytes.Load(),
		})

		pageAssets, links, attachments, _, err := downloadPage(
			fetcher, httpFetcher, opts, item.URL, domain, downloadedAssets, &totalBytes, events,
		)
		if err != nil {
			// Treat user-initiated cancellation as a soft stop, not an error.
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				cancelled = true
				break
			}
			logReportError(item.URL, err)
			emit(PageEvent{
				URL:         item.URL,
				Status:      "error",
				Depth:       item.Depth,
				Pages:       pageCount,
				Assets:      assetCount,
				Attachments: attachmentCount,
				Bytes:       totalBytes.Load(),
			})
			continue
		}
		pageCount++
		assetCount += pageAssets

		if opts.DownloadAll {
			for _, attURL := range attachments {
				if err := opts.Pause.Wait(ctx); err != nil {
					cancelled = true
					break loop
				}
				if _, ok := attachmentsSeen[attURL]; ok {
					continue
				}
				attachmentsSeen[attURL] = struct{}{}
				if dl, ok := downloadAssetFileWithLimit(httpFetcher, opts.OutputDir, attURL, opts.MaxFileBytes, downloadedAssets); ok {
					attachmentCount++
					totalBytes.Add(int64(len(dl.body)))
				}
				events.OnAsset(attURL, "attachment")
				if opts.MaxTotalBytes > 0 && totalBytes.Load() >= opts.MaxTotalBytes {
					break
				}
			}
		}

		emit(PageEvent{
			URL:         item.URL,
			Status:      "done",
			Depth:       item.Depth,
			Pages:       pageCount,
			Assets:      assetCount,
			Attachments: attachmentCount,
			Bytes:       totalBytes.Load(),
		})
		log.Printf("[PROGRESS] Strony: %d, Assety: %d, Zalaczniki: %d, Bajty: %d",
			pageCount, assetCount, attachmentCount, totalBytes.Load())

		if item.Depth < opts.MaxDepth {
			for _, link := range links {
				if _, ok := visited[link]; ok {
					continue
				}
				queue = append(queue, queueItem{URL: link, Depth: item.Depth + 1})
			}
		}

		// small delay to keep the UI smooth, mirrors the original behaviour
		select {
		case <-ctx.Done():
			cancelled = true
			break loop
		case <-time.After(500 * time.Millisecond):
		}
	}

	reportPath := filepath.Join(opts.OutputDir, "download-report.json")
	summary := Summary{
		Pages:       pageCount,
		Assets:      assetCount,
		Attachments: attachmentCount,
		Bytes:       totalBytes.Load(),
		OutputDir:   opts.OutputDir,
		Cancelled:   cancelled,
		ReportPath:  reportPath,
	}
	writeReport(reportPath, opts, summary, reportErrors)
	events.OnComplete(summary)
	return summary, nil
}

func writeReport(path string, opts Options, summary Summary, errs []map[string]string) {
	report := map[string]any{
		"url":         opts.URL,
		"outputDir":   opts.OutputDir,
		"pages":       summary.Pages,
		"assets":      summary.Assets,
		"attachments": summary.Attachments,
		"bytes":       summary.Bytes,
		"cancelled":   summary.Cancelled,
		"finishedAt":  time.Now().Format(time.RFC3339),
		"errors":      errs,
	}
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o644)
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

func sameScopeHost(host, baseDomain string, opts Options) bool {
	if strings.EqualFold(host, baseDomain) {
		return true
	}
	return opts.IncludeSubdomains && isSubdomain(host, baseDomain)
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

// downloadPage fetches a single page, downloads all of its relative assets
// (rewriting the HTML in place), and classifies outbound <a href> links
// into "follow" links and (when DownloadAll is true) attachments.
func downloadPage(
	fetcher pageFetcher,
	httpFetcher *Fetcher,
	opts Options,
	pageURL, domain string,
	downloadedAssets map[string]struct{},
	totalBytes *atomic.Int64,
	events Events,
) (pageAssets int, links, attachments []string, pageBytes int64, err error) {
	if err := opts.Pause.Wait(opts.Context); err != nil {
		return 0, nil, nil, 0, err
	}
	resp, fetchErr := fetcher.FetchPage(pageURL)
	if fetchErr != nil {
		return 0, nil, nil, 0, fmt.Errorf("fetch: %w", fetchErr)
	}
	if !resp.OK {
		return 0, nil, nil, 0, fmt.Errorf("http %d", resp.Status)
	}
	if !resp.IsHTML() {
		// Not an HTML page — treat as a regular asset download, no further
		// processing. Caller will count it as an attachment.
		if dl, ok := downloadAssetFileWithLimit(httpFetcher, opts.OutputDir, pageURL, opts.MaxFileBytes, downloadedAssets); ok {
			pageBytes = int64(len(dl.body))
			totalBytes.Add(pageBytes)
		}
		return 0, nil, nil, pageBytes, nil
	}

	doc, refs, parseErr := RewriteAssets(string(resp.Body))
	if parseErr != nil {
		return 0, nil, nil, 0, fmt.Errorf("parse html: %w", parseErr)
	}
	pageBytes = int64(len(resp.Body))
	totalBytes.Add(pageBytes)

	pagePath, fpErr := FilePathFor(opts.OutputDir, pageURL)
	if fpErr != nil {
		return 0, nil, nil, 0, fmt.Errorf("page path: %w", fpErr)
	}
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o755); err != nil {
		return 0, nil, nil, 0, fmt.Errorf("mkdir: %w", err)
	}

	// Download assets and rewrite references. CSS files are processed
	// recursively (their url(...) / @import references are downloaded too).
	for _, ref := range refs {
		if err := opts.Pause.Wait(opts.Context); err != nil {
			return pageAssets, links, attachments, pageBytes, err
		}
		assetURL, absErr := absoluteURL(pageURL, ref.URL)
		if absErr != nil {
			continue
		}
		// Filter out off-domain assets unless the user explicitly asked
		// for them.
		if isEmbeddedDocumentRef(ref) && !sameScopeAssetURL(assetURL, domain, opts) {
			continue
		}
		if !shouldDownloadAsset(assetURL, domain, opts) {
			continue
		}
		dl, ok := downloadAssetFileWithLimit(httpFetcher, opts.OutputDir, assetURL, opts.MaxFileBytes, downloadedAssets)
		if !ok {
			continue
		}
		downloadedAssets[assetURL] = struct{}{}
		totalBytes.Add(int64(len(dl.body)))

		// If this is a CSS file, parse it for nested url() / @import
		// references. The file is rewritten on disk so those references
		// resolve to local assets.
		if isCSS(dl.path, dl.contentType) {
			if added, cerr := rewriteCSSFile(httpFetcher, dl.path, string(dl.body), assetURL, opts, domain, downloadedAssets, totalBytes, events); cerr == nil {
				pageAssets += added
			}
		}

		rel, relErr := filepath.Rel(filepath.Dir(pagePath), dl.path)
		if relErr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		RewriteAssetURL(doc, ref.Tag, ref.Attr, ref.URL, rel)
		pageAssets++
		events.OnAsset(assetURL, "asset")
	}

	// Rewrite same-scope navigation to the local mirror and classify what the
	// crawler should visit/download next.
	links, attachments = RewriteLocalNavigation(doc, pageURL, opts.OutputDir, domain, opts)

	// Rewrite meta refresh: <meta http-equiv="refresh" content="0; url=/foo">
	RewriteMetaRefresh(doc, pageURL, opts.OutputDir)

	// Write final HTML.
	htmlOut, err := doc.Html()
	if err != nil {
		return pageAssets, links, attachments, pageBytes, fmt.Errorf("render html: %w", err)
	}
	if err := os.WriteFile(pagePath, []byte(htmlOut), 0o644); err != nil {
		return pageAssets, links, attachments, pageBytes, fmt.Errorf("write html: %w", err)
	}
	return pageAssets, links, attachments, pageBytes, nil
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
	if strings.EqualFold(filepath.Ext(path), ".css") {
		return true
	}
	return strings.Contains(strings.ToLower(contentType), "text/css")
}

// downloadResult bundles the on-disk path, the bytes we read and the
// content-type header — everything the caller needs to decide what to do
// next.
type downloadResult struct {
	path        string
	body        []byte
	contentType string
}

// downloadAssetFileWithLimit downloads an asset with an optional per-file
// size cap. The downloaded file is also recorded in `seen` (if non-nil) so
// the caller can dedupe across pages.
func downloadAssetFileWithLimit(fetcher *Fetcher, baseDir, assetURL string, maxBytes int64, seen map[string]struct{}) (downloadResult, bool) {
	if seen != nil {
		if _, dup := seen[assetURL]; dup {
			return downloadResult{}, false
		}
	}
	fullPath, pathErr := AssetPathFor(baseDir, assetURL)
	if pathErr != nil {
		return downloadResult{}, false
	}
	if info, err := os.Stat(fullPath); err == nil && info.Size() > 0 {
		if seen != nil {
			seen[assetURL] = struct{}{}
		}
		return downloadResult{path: fullPath, body: nil, contentType: ""}, true
	}
	resp, err := fetcher.FetchURLWithLimit(assetURL, maxBytes)
	if err != nil || !resp.OK {
		return downloadResult{}, false
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return downloadResult{}, false
	}
	if err := os.WriteFile(fullPath, resp.Body, 0o644); err != nil {
		return downloadResult{}, false
	}
	if seen != nil {
		seen[assetURL] = struct{}{}
	}
	return downloadResult{path: fullPath, body: resp.Body, contentType: resp.ContentType}, true
}

// downloadAssetFile is a backwards-compatible thin wrapper used by
// downloadPage when the caller doesn't care about the file content.
func downloadAssetFile(fetcher *Fetcher, baseDir, assetURL string) (string, bool) {
	dl, ok := downloadAssetFileWithLimit(fetcher, baseDir, assetURL, 0, nil)
	if !ok {
		return "", false
	}
	return dl.path, true
}

func absoluteURL(base, ref string) (string, error) {
	baseParsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	refParsed, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return baseParsed.ResolveReference(refParsed).String(), nil
}

// ErrInvalidURL is returned when the user passes a malformed URL.
var ErrInvalidURL = errors.New("invalid url")
