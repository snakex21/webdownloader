package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/asrock/webdownloader/internal/wv2host"
)

// Engine selects the browser that renders pages in ModeBrowser.
type Engine string

const (
	// EngineAuto tries WebView2, then Edge, then Chrome.
	EngineAuto Engine = ""
	// EngineWebView2 uses a private, hidden instance of the WebView2
	// runtime the app itself runs on (no separate browser needed).
	EngineWebView2 Engine = "webview2"
	// EngineEdge and EngineChrome launch an installed browser headless.
	EngineEdge   Engine = "edge"
	EngineChrome Engine = "chrome"
)

// ErrNoBrowser is returned when no rendering engine could be started.
var ErrNoBrowser = errors.New("no browser engine available: WebView2 runtime, Microsoft Edge or Google Chrome is required for the accurate mode")

const (
	renderTimeout     = 60 * time.Second
	networkIdleWindow = 700 * time.Millisecond
	maxIdleWait       = 12 * time.Second
	bodyFetchWait     = 15 * time.Second
)

// capturedResource is one network response seen while rendering a page.
type capturedResource struct {
	URL      string
	Status   int
	MimeType string
	Type     network.ResourceType
	Body     []byte
}

// renderResult is what the browser saw for one page.
type renderResult struct {
	Engine       string
	DocumentURL  string
	Status       int
	RenderedHTML string
	// Links are the absolute a[href]/area[href] values of the rendered DOM,
	// including links that scripts generated.
	Links     []string
	Resources []capturedResource
}

// pageCapture collects network events for the page being rendered.
type pageCapture struct {
	mu       sync.Mutex
	maxBytes int64
	inflight map[network.RequestID]struct{}
	meta     map[network.RequestID]*capturedResource
	order    []network.RequestID
	pending  []network.RequestID // finished, body not fetched yet
	lastNet  time.Time
	wake     chan struct{}
}

func newPageCapture(maxBytes int64) *pageCapture {
	return &pageCapture{
		maxBytes: maxBytes,
		inflight: map[network.RequestID]struct{}{},
		meta:     map[network.RequestID]*capturedResource{},
		lastNet:  time.Now(),
		wake:     make(chan struct{}, 1),
	}
}

func (c *pageCapture) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *pageCapture) handle(ev any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		if e.Request == nil || !isHTTPURL(e.Request.URL) {
			return
		}
		c.inflight[e.RequestID] = struct{}{}
		c.lastNet = time.Now()
	case *network.EventResponseReceived:
		if e.Response == nil || !isHTTPURL(e.Response.URL) {
			return
		}
		if _, ok := c.meta[e.RequestID]; !ok {
			c.order = append(c.order, e.RequestID)
		}
		c.meta[e.RequestID] = &capturedResource{
			URL:      e.Response.URL,
			Status:   int(e.Response.Status),
			MimeType: e.Response.MimeType,
			Type:     e.Type,
		}
		c.lastNet = time.Now()
	case *network.EventLoadingFinished:
		delete(c.inflight, e.RequestID)
		c.lastNet = time.Now()
		// 206 is a partial body (media range request): never save it as a file.
		if m, ok := c.meta[e.RequestID]; ok && m.Status >= 200 && m.Status < 300 && m.Status != 206 {
			if c.maxBytes > 0 && int64(e.EncodedDataLength) > c.maxBytes {
				return
			}
			c.pending = append(c.pending, e.RequestID)
			c.signal()
		}
	case *network.EventLoadingFailed:
		delete(c.inflight, e.RequestID)
		c.lastNet = time.Now()
	}
}

func isHTTPURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// browserFetcher renders pages in a real browser engine and records every
// network response, so modules, fetch()ed JSON, lazy images and fonts end
// up in the offline copy.
type browserFetcher struct {
	http   *Fetcher
	engine string
	host   *wv2host.Host

	allocCtx    context.Context
	allocCancel context.CancelFunc
	tabCtx      context.Context
	tabCancel   context.CancelFunc
	output      *bytes.Buffer
	profileDir  string

	maxBytes int64
	current  atomic.Pointer[pageCapture]
	enabled  bool
}

// newBrowserFetcher starts the requested engine (EngineAuto picks the first
// that works) and opens one tab that is reused for every page.
func newBrowserFetcher(httpFetcher *Fetcher, parentCtx context.Context, engine Engine, maxBytes int64) (*browserFetcher, error) {
	var order []Engine
	switch engine {
	case EngineAuto:
		order = []Engine{EngineWebView2, EngineEdge, EngineChrome}
	default:
		order = []Engine{engine}
	}
	cleanupStaleProfiles()
	var errs []error
	for _, e := range order {
		bf, err := startEngine(httpFetcher, parentCtx, e, maxBytes)
		if err == nil {
			return bf, nil
		}
		if parentCtx.Err() != nil {
			return nil, parentCtx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", e, err))
	}
	return nil, fmt.Errorf("%w (%v)", ErrNoBrowser, errors.Join(errs...))
}

func startEngine(httpFetcher *Fetcher, parentCtx context.Context, engine Engine, maxBytes int64) (*browserFetcher, error) {
	bf := &browserFetcher{http: httpFetcher, engine: string(engine), maxBytes: maxBytes, output: &bytes.Buffer{}}
	var tabOpts []chromedp.ContextOption

	switch engine {
	case EngineWebView2:
		host, err := wv2host.Start(20 * time.Second)
		if err != nil {
			return nil, err
		}
		bf.host = host
		bf.engine = "webview2 " + host.Version
		targetID, err := firstPageTarget(host.DevToolsURL)
		if err != nil {
			host.Close()
			return nil, err
		}
		bf.allocCtx, bf.allocCancel = chromedp.NewRemoteAllocator(parentCtx, host.DevToolsURL, chromedp.NoModifyURL)
		// WebView2 cannot open new tabs; drive the page it already has.
		tabOpts = append(tabOpts, chromedp.WithTargetID(target.ID(targetID)))
	case EngineEdge, EngineChrome:
		binPath, err := findBrowserFor(engine)
		if err != nil {
			return nil, err
		}
		profileDir, err := os.MkdirTemp("", "webdownloader-chrome-*")
		if err != nil {
			return nil, fmt.Errorf("create temporary browser profile: %w", err)
		}
		bf.profileDir = profileDir
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(binPath),
			chromedp.Flag("headless", true),
			chromedp.DisableGPU,
			chromedp.Flag("disable-extensions", true),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
			chromedp.Flag("disable-sync", true),
			chromedp.Flag("mute-audio", true),
			chromedp.WindowSize(1366, 900),
			chromedp.UserDataDir(profileDir),
			chromedp.CombinedOutput(bf.output),
		)
		bf.allocCtx, bf.allocCancel = chromedp.NewExecAllocator(parentCtx, opts...)
	default:
		return nil, fmt.Errorf("unknown engine %q", engine)
	}

	bf.tabCtx, bf.tabCancel = chromedp.NewContext(bf.allocCtx, tabOpts...)
	chromedp.ListenTarget(bf.tabCtx, func(ev any) {
		if c := bf.current.Load(); c != nil {
			c.handle(ev)
		}
	})
	// The first Run binds the tab to the context it gets, so it must be
	// tabCtx itself (a derived timeout context would close the tab when
	// cancelled); the start-up timeout is enforced separately.
	runErr := make(chan error, 1)
	go func() {
		runErr <- chromedp.Run(bf.tabCtx,
			network.Enable().WithMaxTotalBufferSize(256<<20).WithMaxResourceBufferSize(64<<20),
			network.SetCacheDisabled(true),
		)
	}()
	var err error
	select {
	case err = <-runErr:
	case <-time.After(30 * time.Second):
		err = errors.New("browser did not respond within 30s")
	}
	if err != nil {
		_ = bf.Close()
		return nil, bf.wrapErr("start", err)
	}
	return bf, nil
}

// firstPageTarget asks the DevTools HTTP endpoint for the existing page.
func firstPageTarget(devToolsURL string) (string, error) {
	base := "http://" + strings.TrimPrefix(strings.SplitN(devToolsURL, "/devtools/", 2)[0], "ws://")
	client := &http.Client{Timeout: 5 * time.Second}
	for range 50 {
		resp, err := client.Get(base + "/json/list")
		if err == nil {
			var targets []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			derr := json.NewDecoder(resp.Body).Decode(&targets)
			_ = resp.Body.Close()
			if derr == nil {
				for _, t := range targets {
					if t.Type == "page" {
						return t.ID, nil
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("WebView2 page target not found")
}

func (b *browserFetcher) wrapErr(what string, err error) error {
	out := strings.TrimSpace(b.output.String())
	if out != "" {
		return fmt.Errorf("%s: %w; browser output: %s", what, err, trimForError(out, 600))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// FetchPage renders url and returns the page's original HTML (as served,
// not the script-modified DOM, so the saved page runs its scripts once) plus
// everything the browser downloaded along the way.
func (b *browserFetcher) FetchPage(pageURL string) (*FetchResult, error) {
	rr, err := b.render(pageURL)
	if err != nil {
		return nil, err
	}
	res := &FetchResult{
		OK:          rr.Status == 0 || (rr.Status >= 200 && rr.Status < 400),
		Status:      rr.Status,
		FinalURL:    rr.DocumentURL,
		ContentType: "text/html; charset=utf-8",
		Rendered:    rr,
	}
	if res.Status == 0 {
		res.Status = 200
	}
	// Prefer the document as the server sent it.
	for i, r := range rr.Resources {
		if r.Type == network.ResourceTypeDocument && sameDocumentURL(r.URL, rr.DocumentURL) {
			res.Body = r.Body
			res.ContentType = r.MimeType
			rr.Resources = append(rr.Resources[:i:i], rr.Resources[i+1:]...)
			break
		}
	}
	if res.Body == nil {
		res.Body = []byte(rr.RenderedHTML)
		res.ContentType = "text/html; charset=utf-8"
	}
	res.Size = int64(len(res.Body))
	return res, nil
}

func sameDocumentURL(a, b string) bool {
	strip := func(s string) string {
		if i := strings.IndexByte(s, '#'); i >= 0 {
			return s[:i]
		}
		return s
	}
	return strip(a) == strip(b)
}

func (b *browserFetcher) render(pageURL string) (*renderResult, error) {
	capture := newPageCapture(b.maxBytes)
	b.current.Store(capture)
	defer b.current.Store(nil)

	pageCtx, cancel := context.WithTimeout(b.tabCtx, renderTimeout)
	defer cancel()

	// Fetch bodies as soon as responses finish: Chromium may evict them
	// from its buffer once the page loads more.
	bodies := map[network.RequestID][]byte{}
	var bodiesMu sync.Mutex
	fetchDone := make(chan struct{})
	stopFetch := make(chan struct{})
	go func() {
		defer close(fetchDone)
		for {
			capture.mu.Lock()
			ids := capture.pending
			capture.pending = nil
			capture.mu.Unlock()
			for _, id := range ids {
				var body []byte
				err := chromedp.Run(pageCtx, chromedp.ActionFunc(func(ctx context.Context) error {
					var e error
					body, e = network.GetResponseBody(id).Do(ctx)
					return e
				}))
				if err == nil {
					bodiesMu.Lock()
					bodies[id] = body
					bodiesMu.Unlock()
				}
			}
			if len(ids) > 0 {
				continue
			}
			select {
			case <-capture.wake:
			case <-stopFetch:
				capture.mu.Lock()
				left := len(capture.pending)
				capture.mu.Unlock()
				if left == 0 {
					return
				}
			case <-pageCtx.Done():
				return
			}
		}
	}()

	var docStatus int
	var docURL string
	err := chromedp.Run(pageCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			resp, err := chromedp.RunResponse(ctx, chromedp.Navigate(pageURL))
			if err != nil {
				return err
			}
			if resp != nil {
				docStatus = int(resp.Status)
				docURL = resp.URL
			}
			return nil
		}),
	)
	if err == nil {
		b.waitNetworkIdle(pageCtx, capture)
		// Nudge lazy loaders (images, infinite lists) once, then let the
		// network settle again.
		_ = chromedp.Run(pageCtx, chromedp.Evaluate(`window.scrollTo(0, document.documentElement.scrollHeight); 0`, nil))
		b.waitNetworkIdle(pageCtx, capture)
	}

	var rendered string
	var links []string
	var location string
	if err == nil {
		err = chromedp.Run(pageCtx,
			chromedp.Evaluate(`location.href`, &location),
			chromedp.Evaluate(`Array.from(document.querySelectorAll('a[href],area[href]'), a => a.href)`, &links),
			chromedp.OuterHTML("html", &rendered, chromedp.ByQuery),
		)
	}

	close(stopFetch)
	select {
	case <-fetchDone:
	case <-time.After(bodyFetchWait):
	}

	if err != nil {
		if errors.Is(pageCtx.Err(), context.Canceled) || errors.Is(b.tabCtx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(pageCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("render: page did not finish loading within %v: %w", renderTimeout, ErrIdleTimeout)
		}
		return nil, b.wrapErr("render", err)
	}

	if docURL == "" {
		docURL = location
	}
	rr := &renderResult{
		Engine:       b.engine,
		DocumentURL:  docURL,
		Status:       docStatus,
		RenderedHTML: rendered,
		Links:        links,
	}
	capture.mu.Lock()
	bodiesMu.Lock()
	for _, id := range capture.order {
		meta := capture.meta[id]
		body, ok := bodies[id]
		if !ok || meta == nil {
			continue
		}
		r := *meta
		r.Body = body
		rr.Resources = append(rr.Resources, r)
	}
	bodiesMu.Unlock()
	capture.mu.Unlock()
	return rr, nil
}

// waitNetworkIdle returns once no request has been active for
// networkIdleWindow, or after maxIdleWait.
func (b *browserFetcher) waitNetworkIdle(ctx context.Context, c *pageCapture) {
	deadline := time.Now().Add(maxIdleWait)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		idle := len(c.inflight) == 0 && time.Since(c.lastNet) >= networkIdleWindow
		c.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// EngineName reports which engine is rendering (for the report).
func (b *browserFetcher) EngineName() string { return b.engine }

// Close shuts the browser down and removes its temporary profile.
func (b *browserFetcher) Close() error {
	if b.tabCancel != nil {
		b.tabCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	if b.host != nil {
		b.host.Close()
	}
	if b.profileDir != "" {
		for range 20 {
			if os.RemoveAll(b.profileDir) == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

func trimForError(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// findBrowserFor returns the path of an installed Edge or Chrome.
func findBrowserFor(engine Engine) (string, error) {
	if env := os.Getenv("WEBDOWNLOADER_BROWSER"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	var candidates, names []string
	switch engine {
	case EngineEdge:
		candidates = edgeCandidates()
		names = []string{"msedge.exe", "msedge", "microsoft-edge"}
	default:
		candidates = chromeCandidates()
		names = []string{"chrome.exe", "chrome", "google-chrome", "chromium", "chromium-browser"}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not installed", engine)
}

func edgeCandidates() []string {
	return []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		expandLocal(`Microsoft\Edge\Application\msedge.exe`),
	}
}

func chromeCandidates() []string {
	return []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		expandLocal(`Google\Chrome\Application\chrome.exe`),
	}
}

func expandLocal(rel string) string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return base + `\` + rel
	}
	return ""
}

// AvailableEngines reports which engines could be used on this machine
// (without starting them).
func AvailableEngines() map[string]bool {
	_, edgeErr := findBrowserFor(EngineEdge)
	_, chromeErr := findBrowserFor(EngineChrome)
	return map[string]bool{
		string(EngineWebView2): wv2host.Available(),
		string(EngineEdge):     edgeErr == nil,
		string(EngineChrome):   chromeErr == nil,
	}
}

// cleanupStaleProfiles removes temporary browser profiles left behind by a
// run that was killed (normally Close removes them). Profiles still in use
// are locked by the browser and fail to delete, which is fine.
func cleanupStaleProfiles() {
	tmp := os.TempDir()
	for _, pattern := range []string{"webdownloader-wv2-*", "webdownloader-chrome-*"} {
		matches, _ := filepath.Glob(filepath.Join(tmp, pattern))
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(m)
			}
		}
	}
}
