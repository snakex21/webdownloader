package downloader

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Download runs a recursive download described by opts and emits progress
// events through events. It returns once every reachable page up to
// MaxDepth has been processed, one of the limits has been reached, or the
// run was cancelled. A failure to save even the start page is reported in
// Summary.Failed (and OnComplete), not as an error.
//
// A run goes through these stages: setup (newCrawler, error log, browser,
// progress ticker), the crawl loop (crawl: take the next page, process it,
// queue its links, fetch its attachments) and finish (totals, report,
// OnComplete).
func Download(opts Options, events Events) (Summary, error) {
	started := time.Now()
	opts = opts.withDefaults()
	events = events.withDefaults()

	c, failKind, err := newCrawler(opts, events, started)
	if err != nil {
		// The run could not even start: the UI still gets its OnComplete
		// (a run that only returned an error left it waiting).
		s := Summary{OutputDir: opts.OutputDir, Failed: true, FailKind: failKind, FailDetail: err.Error(), Elapsed: time.Since(started)}
		events.OnComplete(s)
		return s, err
	}
	defer c.http.Cancel()

	c.openErrorLog()
	if !c.startBrowser() {
		return c.finish()
	}
	if c.browser != nil {
		defer c.browser.Close()
	}
	c.startTicker()
	defer c.stopTicker()

	c.crawl()
	return c.finish()
}

// ---- Setup -------------------------------------------------------------

// withDefaults fills in the defaults of unset options.
func (opts Options) withDefaults() Options {
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
	return opts
}

// withDefaults replaces missing callbacks with no-ops.
func (events Events) withDefaults() Events {
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
	return events
}

// newCrawler validates the start URL, creates the output folder and sets up
// the state of one run. On error it returns the FailKind for the summary.
func newCrawler(opts Options, events Events, started time.Time) (*crawler, string, error) {
	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return nil, "invalid", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, "invalid", fmt.Errorf("%w: expected an absolute HTTP(S) URL", ErrInvalidURL)
	}
	site := layout{dir: opts.OutputDir, site: parsed.Hostname()}
	startFile, err := site.pagePath(opts.URL)
	if err != nil {
		return nil, "invalid", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	_, statErr := os.Stat(opts.OutputDir)
	createdDir := os.IsNotExist(statErr)
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, "write", fmt.Errorf("create output dir: %w", &localWriteError{err: err})
	}

	c := &crawler{
		opts:             opts,
		domain:           parsed.Hostname(),
		layout:           site,
		events:           events,
		seen:             make(map[string]struct{}),
		seenPaths:        make(map[string]struct{}),
		jsLiterals:       make(map[string][]string),
		scanned:          make(map[string]struct{}),
		speculativeTried: make(map[string]struct{}),
		failed:           make(map[string]struct{}),
		guesses:          make(map[string]*guessStat),
		skippedExternal:  make(map[string]int),
		started:          started,
		startFile:        startFile,
		createdDir:       createdDir,
		summary:          Summary{OutputDir: opts.OutputDir, ReportPath: filepath.Join(opts.OutputDir, "download-report.json")},
		stopTicker:       func() {},
		attachmentsSeen:  make(map[string]struct{}),
	}
	c.http = NewFetcherWithContext(opts.Context, opts.Retries)
	c.http.SetCookie(opts.Cookie, opts.URL)
	c.ctx = c.http.ctx
	c.fetcher = c.http
	c.queue = pageQueue{
		queued:       make(map[string]struct{}),
		visited:      make(map[string]struct{}),
		visitedFiles: make(map[string]struct{}),
	}
	c.enqueue(opts.URL, 0, false)
	return c, "", nil
}

// openErrorLog opens download-errors.log (one line per failure, best
// effort) and routes every OnError through the report as well.
func (c *crawler) openErrorLog() {
	if root, err := os.OpenRoot(c.opts.OutputDir); err == nil {
		if f, ferr := root.OpenFile("download-errors.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
			c.errLog = f
		}
		root.Close()
	}
	uiOnError := c.events.OnError
	c.events.OnError = func(pageURL string, err error) {
		c.reportErrors = append(c.reportErrors, map[string]string{"url": pageURL, "error": err.Error()})
		if len(c.failedFiles) < maxFailedListed {
			kind, status := classifyError(err)
			c.failedFiles = append(c.failedFiles, FailedFile{URL: pageURL, Kind: kind, Status: status})
		}
		uiOnError(pageURL, err)
		if c.errLog != nil {
			line := time.Now().Format(time.RFC3339) + " " + pageURL + " " + err.Error() + "\n"
			_, _ = c.errLog.WriteString(line)
		}
	}
}

// startBrowser starts the browser engine in browser mode. It returns false
// when the run must finish right away (cancelled or no engine).
func (c *crawler) startBrowser() bool {
	if c.opts.Mode != ModeBrowser {
		return true
	}
	bf, err := newBrowserFetcher(c.http, c.ctx, c.opts.Engine, c.opts.MaxFileBytes)
	if err != nil {
		if c.ctx.Err() != nil {
			c.summary.Cancelled = true
			return false
		}
		c.events.OnError(c.opts.URL, err)
		c.summary.Failed = true
		c.summary.FailKind = "browser"
		c.summary.FailDetail = err.Error()
		return false
	}
	c.browser = bf
	c.fetcher = bf
	c.summary.Engine = bf.EngineName()
	return true
}

// startTicker reports bytes and the current file about twice a second
// while the crawl is busy inside one page or one large file. stopTicker
// waits for it to end, so no "progress" event can follow OnComplete.
func (c *crawler) startTicker() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-c.ctx.Done():
				return
			case <-t.C:
				c.emitPage(c.snapshot("progress"))
			}
		}
	}()
	var once sync.Once
	c.stopTicker = func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

// ---- Crawl loop ----------------------------------------------------------

// queueItem is one page waiting to be downloaded.
type queueItem struct {
	URL         string
	Depth       int
	Speculative bool // guessed from a script literal: fails silently
}

// pageQueue is the breadth-first queue of pages. Pages are queued once per
// output file: "page#a", "page#b", "/" and "/index.html", http and https
// are one page, so the queue length (and the UI's percentage) counts only
// what will really be downloaded.
type pageQueue struct {
	items        []queueItem
	queued       map[string]struct{} // pageKey of every page ever queued
	visited      map[string]struct{} // URLs taken from the queue
	visitedFiles map[string]struct{} // "/" and "/index.html" are one page
}

// enqueue adds a page unless one saved to the same file was queued before.
func (c *crawler) enqueue(pageURL string, depth int, speculative bool) {
	key := c.pageKey(pageURL)
	if _, ok := c.queue.queued[key]; ok {
		return
	}
	c.queue.queued[key] = struct{}{}
	c.queue.items = append(c.queue.items, queueItem{URL: pageURL, Depth: depth, Speculative: speculative})
}

// crawl processes queued pages until the queue is empty, a limit is hit or
// the run is cancelled (recorded in c.summary).
func (c *crawler) crawl() {
	for c.canContinue() {
		item := c.queue.items[0]
		c.queue.items = c.queue.items[1:]
		if !c.claim(item) {
			continue
		}
		if !c.processPage(item) {
			return
		}
	}
}

// canContinue waits while paused and reports whether another page may be
// taken from the queue.
func (c *crawler) canContinue() bool {
	if err := c.opts.Pause.Wait(c.ctx); err != nil {
		c.summary.Cancelled = true
		return false
	}
	if c.ctx.Err() != nil {
		c.summary.Cancelled = true
		return false
	}
	if c.opts.MaxPages > 0 && int(c.pagesDone.Load()) >= c.opts.MaxPages {
		return false
	}
	if c.opts.MaxTotalBytes > 0 && c.totalBytes.Load() >= c.opts.MaxTotalBytes {
		return false
	}
	return len(c.queue.items) > 0
}

// claim reports whether item still has to be downloaded and marks it as
// visited.
func (c *crawler) claim(item queueItem) bool {
	if _, ok := c.queue.visited[item.URL]; ok {
		return false
	}
	if item.Depth >= c.opts.MaxDepth {
		return false
	}
	c.queue.visited[item.URL] = struct{}{}
	if p, err := c.layout.pagePath(item.URL); err == nil {
		key := strings.ToLower(p)
		if _, dup := c.queue.visitedFiles[key]; dup {
			return false
		}
		c.queue.visitedFiles[key] = struct{}{}
	}
	// Pages guessed from script literals are probed over plain HTTP first:
	// rendering a 404 in the browser would cost seconds each.
	if item.Speculative && c.opts.Mode == ModeBrowser && !c.probePage(item.URL) {
		c.speculativeMiss++
		return false
	}
	return true
}

// processPage downloads one page with its assets, queues its links and
// fetches its attachments. It returns false when the crawl must stop.
func (c *crawler) processPage(item queueItem) bool {
	c.emit(item, "downloading")

	c.discoveredPages = nil
	c.pageSpeculative = 0
	links, attachments, err := c.downloadPage(item.URL)
	if err != nil {
		return c.pageFailed(item, err)
	}
	c.pagesDone.Add(1)

	// Queue the links first, so progress knows what is left while the
	// attachments (possibly large) download.
	if item.Depth+1 < c.opts.MaxDepth {
		for _, link := range links {
			c.enqueue(link, item.Depth+1, false)
		}
		for _, link := range c.discoveredPages {
			c.enqueue(link, item.Depth+1, true)
		}
	}
	c.queueLen.Store(int64(len(c.queue.items)))

	if c.opts.DownloadAll && !c.fetchAttachments(attachments) {
		return false
	}

	c.emit(item, "done")
	log.Printf("[PROGRESS] pages=%d assets=%d attachments=%d bytes=%d queued=%d",
		c.pagesDone.Load(), c.assetsN.Load(), c.attachments.Load(), c.totalBytes.Load(), len(c.queue.items))
	return c.politeDelay()
}

// pageFailed records a page that could not be saved. It returns false when
// the failure was the run being cancelled.
func (c *crawler) pageFailed(item queueItem, err error) bool {
	if errors.Is(err, context.Canceled) || c.ctx.Err() != nil {
		c.summary.Cancelled = true
		return false
	}
	if item.Speculative {
		c.speculativeMiss++
		return true
	}
	c.events.OnError(item.URL, err)
	if item.Depth == 0 && c.pagesDone.Load() == 0 {
		c.summary.Failed = true
		c.summary.FailKind, c.summary.FailStatus = classifyError(err)
		c.summary.FailDetail = err.Error()
	}
	c.emit(item, "error")
	return true
}

// fetchAttachments downloads the attachments (PDF, ZIP, ...) linked from a
// page. It returns false when the run was cancelled meanwhile.
func (c *crawler) fetchAttachments(attachments []string) bool {
	for _, attURL := range attachments {
		if err := c.opts.Pause.Wait(c.ctx); err != nil {
			c.summary.Cancelled = true
			return false
		}
		if _, ok := c.attachmentsSeen[attURL]; ok {
			continue
		}
		c.attachmentsSeen[attURL] = struct{}{}
		c.setCurrent(attURL)
		dl, err := c.download(attURL)
		if err != nil {
			reportAssetError(c.ctx, c.events, attURL, err)
		} else if !dl.reused {
			c.attachments.Add(1)
			c.totalBytes.Add(dl.size)
		}
		c.events.OnAsset(attURL, "attachment")
		if c.opts.MaxTotalBytes > 0 && c.totalBytes.Load() >= c.opts.MaxTotalBytes {
			break
		}
	}
	return true
}

// politeDelay pauses between pages. It returns false when the run was
// cancelled during the pause.
func (c *crawler) politeDelay() bool {
	if c.opts.PageDelay <= 0 || len(c.queue.items) == 0 {
		return true
	}
	select {
	case <-c.ctx.Done():
		c.summary.Cancelled = true
		return false
	case <-time.After(c.opts.PageDelay):
		return true
	}
}

// emit reports a page lifecycle moment with the live counters.
func (c *crawler) emit(item queueItem, status string) {
	c.queueLen.Store(int64(len(c.queue.items)))
	ev := c.snapshot(status)
	ev.URL, ev.Depth = item.URL, item.Depth
	c.emitPage(ev)
}

// ---- Finish --------------------------------------------------------------

// finish stops the ticker, fills in the totals, writes the report (or
// removes the folder of a run that saved nothing) and sends OnComplete.
func (c *crawler) finish() (Summary, error) {
	c.stopTicker()
	summary := c.summary
	summary.Bytes = c.totalBytes.Load()
	summary.Assets = int(c.assetsN.Load())
	summary.Pages = int(c.pagesDone.Load())
	summary.Attachments = int(c.attachments.Load())
	summary.Errors = len(c.reportErrors)
	summary.SkippedExternal, summary.SkippedExternalHosts, summary.SkippedTrackers, summary.SkippedTrackerHosts = c.skippedTotals()
	summary.FailedFiles = c.failedFiles
	summary.NeedsServer = c.needsServer
	summary.Elapsed = time.Since(c.started)
	if summary.Pages > 0 {
		if _, err := os.Stat(c.startFile); err == nil {
			summary.StartFile = c.startFile
		}
	}
	if c.errLog != nil {
		_ = c.errLog.Close()
	}
	if summary.Failed && c.createdDir && onlyRunMetadata(c.opts.OutputDir) {
		// Nothing was saved: do not leave an empty folder behind.
		_ = os.RemoveAll(c.opts.OutputDir)
		summary.ReportPath = ""
	} else {
		writeReport(summary.ReportPath, c.opts, summary, c.reportErrors, c.speculativeMiss)
	}
	c.events.OnComplete(summary)
	return summary, nil
}
