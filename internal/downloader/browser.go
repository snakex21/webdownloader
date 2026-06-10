package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// browserFetcher is the pageFetcher implementation that renders pages
// through a headless Chromium-compatible browser (Edge or Chrome). It
// reuses the HTTP fetcher for sub-resources.
type browserFetcher struct {
	http    *Fetcher
	ctx     context.Context
	cancel  context.CancelFunc
	browser *Browser
}

// newBrowserFetcher detects an installed Edge / Chrome, spawns a
// headless instance, and returns a fetcher that hands back the rendered
// HTML for every FetchPage call.
func newBrowserFetcher(httpFetcher *Fetcher, parentCtx context.Context) (*browserFetcher, error) {
	binPath, err := findBrowser()
	if err != nil {
		return nil, err
	}
	profileDir, err := os.MkdirTemp("", "webdownloader-chrome-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary browser profile: %w", err)
	}
	var chromeOutput bytes.Buffer

	// 60-second render budget per page. SPAs that genuinely need more
	// than that are out of scope for a static downloader.
	renderTimeout := 60 * time.Second

	// Allocate the browser with a small flag set so we don't keep
	// cookies / cache between runs.
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(binPath),
		chromedp.Flag("headless", true),
		chromedp.DisableGPU,
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.UserDataDir(profileDir),
		chromedp.CombinedOutput(&chromeOutput),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)

	// The browser context is created lazily on the first FetchPage call
	// so a misconfigured install (no Chrome at all) can be reported
	// without spinning up a child process.
	bf := &browserFetcher{
		http:   httpFetcher,
		ctx:    allocCtx,
		cancel: allocCancel,
		browser: &Browser{
			timeout:    renderTimeout,
			profileDir: profileDir,
			output:     &chromeOutput,
		},
	}
	return bf, nil
}

// FetchPage navigates to url in the headless browser, waits for the
// network to go idle, and returns the rendered HTML.
func (b *browserFetcher) FetchPage(url string) (*FetchResult, error) {
	// Lazily create the browser context.
	if b.browser.context == nil {
		browserCtx, browserCancel := chromedp.NewContext(b.ctx)
		b.browser.context = browserCtx
		b.browser.cancel = browserCancel
	}
	// Each page gets its own target context with a real timeout. This context is
	// a child of the browser context, which is a child of the UI cancellation
	// context; both user-cancel and timeout now actually stop chromedp.Run.
	targetCtx, targetCancel := chromedp.NewContext(b.browser.context)
	defer targetCancel()
	pageCtx, pageCancel := context.WithTimeout(targetCtx, b.browser.timeout)
	defer pageCancel()

	var html string
	err := chromedp.Run(pageCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		// Wait for at least one tick of network idle.
		chromedp.Sleep(500*time.Millisecond),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		// Re-throw cancellation/timeout as context errors so the caller can
		// distinguish user cancellation from render failures.
		if errors.Is(pageCtx.Err(), context.Canceled) || errors.Is(pageCtx.Err(), context.DeadlineExceeded) {
			return nil, pageCtx.Err()
		}
		out := strings.TrimSpace(b.browser.output.String())
		if out != "" {
			return nil, fmt.Errorf("render: %w; browser output: %s", err, trimForError(out, 900))
		}
		return nil, fmt.Errorf("render: %w", err)
	}
	return &FetchResult{
		OK:          true,
		Status:      200,
		FinalURL:    url,
		Body:        []byte(html),
		ContentType: "text/html; charset=utf-8",
	}, nil
}

// Close shuts the browser down.
func (b *browserFetcher) Close() error {
	if b.browser != nil && b.browser.cancel != nil {
		b.browser.cancel()
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.browser != nil && b.browser.profileDir != "" {
		_ = os.RemoveAll(b.browser.profileDir)
	}
	return nil
}

// Browser is the per-fetcher browser instance. Kept in a small struct so
// we can grow it (e.g. per-page timeouts) without touching the
// pageFetcher interface.
type Browser struct {
	context    context.Context
	cancel     context.CancelFunc
	timeout    time.Duration
	profileDir string
	output     *bytes.Buffer
}

func trimForError(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// findBrowser returns the path to a usable headless browser, looking at
// Edge first (preinstalled on every Windows 10/11 with WebView2, which
// this app already depends on) and then Chrome.
func findBrowser() (string, error) {
	candidates := browserCandidates()
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	// Last resort: rely on PATH.
	for _, name := range []string{"msedge.exe", "chrome.exe", "chromium.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New(
		"headless browser not found: install Microsoft Edge or Google Chrome to use the browser mode",
	)
}

func browserCandidates() []string {
	candidates := []string{
		// Microsoft Edge (always present where WebView2 is).
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		// Google Chrome stable / beta / dev / canary.
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	// Honour the user's explicit override (set by the UI on first error).
	if env := os.Getenv("WEBDOWNLOADER_BROWSER"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	// Deduplicate (the function is order-sensitive so we keep the
	// priority from the slice above).
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		k := strings.ToLower(c)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}
