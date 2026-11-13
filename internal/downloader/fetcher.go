package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	maxRedirects   = 10
	maxRetryAfter  = 10 * time.Second
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// FetchResult is the result of a single HTTP fetch.
type FetchResult struct {
	OK          bool
	Status      int
	FinalURL    string
	Body        []byte // nil for FetchToFile
	Size        int64  // bytes read (FetchURL*) or written (FetchToFile)
	ContentType string
	// Rendered is set by the browser fetcher: what the page loaded.
	Rendered *renderResult
}

// ErrFileTooLarge is returned before/while reading a response whose size
// exceeds the configured per-file limit.
var ErrFileTooLarge = errors.New("file exceeds configured size limit")

// ErrIdleTimeout is returned when a server stops sending data for longer
// than the fetcher's timeout (while waiting for headers or mid-body).
var ErrIdleTimeout = errors.New("timed out waiting for data")

// Fetcher is a reusable HTTP client that follows redirects, has a UA header
// and aborts requests that stall. All in-flight requests can be cancelled
// at once via Cancel().
type Fetcher struct {
	client       *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	retries      int
	timeout      time.Duration // header wait and max gap between body reads
	backoff      time.Duration // first retry delay, doubled per attempt
	cookie       string
	cookieOrigin *url.URL
}

// NewFetcher creates a new Fetcher. The context is the one used for every
// FetchURL call until Cancel is invoked.
func NewFetcher() *Fetcher {
	return NewFetcherWithContext(context.Background(), 0)
}

// NewFetcherWithContext creates a new Fetcher tied to parent. Cancelling the
// parent context (for example from the UI Cancel button) aborts all in-flight
// and future requests.
func NewFetcherWithContext(parent context.Context, retries int) *Fetcher {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if retries < 0 {
		retries = 0
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultTimeout
	return &Fetcher{
		client: &http.Client{
			// No Client.Timeout: it caps the whole transfer and would kill
			// large downloads on slow links. Stalls are caught by the header
			// timeout above and by the idle timer in open().
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				// Go otherwise forwards sensitive headers to same-host redirects,
				// including different ports and HTTPS-to-HTTP downgrades.
				if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
					req.Header.Del("Cookie")
				}
				return nil
			},
		},
		ctx:     ctx,
		cancel:  cancel,
		retries: retries,
		timeout: defaultTimeout,
		backoff: 500 * time.Millisecond,
	}
}

// SetCookie binds a manually supplied cookie to the starting origin only.
// Unlike a cookie jar, a pasted Cookie header has no trustworthy Domain or
// Secure attributes, so external assets must never inherit it.
func (f *Fetcher) SetCookie(cookie, originURL string) {
	f.cookie = strings.TrimSpace(cookie)
	f.cookieOrigin, _ = url.Parse(originURL)
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// Cancel aborts every in-flight request and prevents new ones from being
// made. It is safe to call multiple times.
func (f *Fetcher) Cancel() {
	if f.cancel != nil {
		f.cancel()
	}
}

// Close releases the fetcher's resources. The HTTP client has no explicit
// Close, so this is a no-op kept to satisfy the pageFetcher interface.
func (f *Fetcher) Close() error { return nil }

// FetchPage is the pageFetcher implementation. The HTTP fetcher simply
// returns the raw HTTP response.
func (f *Fetcher) FetchPage(rawURL string) (*FetchResult, error) {
	return f.FetchURL(rawURL)
}

// FetchURL downloads the resource at the given URL and returns its body.
// It resolves redirects, applies timeouts, a desktop User-Agent header and
// is cancellable through Fetcher.Cancel.
func (f *Fetcher) FetchURL(rawURL string) (*FetchResult, error) {
	return f.FetchURLWithLimit(rawURL, 0)
}

// FetchURLWithLimit is like FetchURL, but enforces maxBytes before reading
// the full response into memory. maxBytes <= 0 means unlimited.
func (f *Fetcher) FetchURLWithLimit(rawURL string, maxBytes int64) (*FetchResult, error) {
	return f.withRetries(func() (*FetchResult, time.Duration, error) {
		resp, err := f.open(rawURL, maxBytes)
		if err != nil {
			return nil, 0, err
		}
		defer resp.body.Close()
		body, err := io.ReadAll(resp.body)
		if err != nil {
			return nil, 0, err
		}
		if maxBytes > 0 && int64(len(body)) > maxBytes {
			return nil, 0, fmt.Errorf("%w: body > %d", ErrFileTooLarge, maxBytes)
		}
		resp.result.Body = body
		resp.result.Size = int64(len(body))
		return resp.result, resp.retryAfter, nil
	})
}

// FetchToFile streams a successful (2xx) response body straight into
// fullPath inside baseDir without buffering it in memory. The file appears
// under its final name only once complete (see writeOutputStream). Non-2xx
// responses are returned with OK=false and nothing is written.
func (f *Fetcher) FetchToFile(rawURL string, maxBytes int64, baseDir, fullPath string) (*FetchResult, error) {
	return f.withRetries(func() (*FetchResult, time.Duration, error) {
		resp, err := f.open(rawURL, maxBytes)
		if err != nil {
			return nil, 0, err
		}
		defer resp.body.Close()
		if !resp.result.OK {
			return resp.result, resp.retryAfter, nil
		}
		src := &trackingReader{r: resp.body, max: maxBytes}
		n, err := writeOutputStream(baseDir, fullPath, src)
		if err != nil {
			if src.err == nil {
				// The network side was fine, so this is a local I/O failure
				// (disk full, access denied, ...). Retrying will not help.
				return nil, 0, &localWriteError{err: err}
			}
			return nil, 0, src.err
		}
		resp.result.Size = n
		return resp.result, 0, nil
	})
}

// localWriteError marks failures writing to disk, as opposed to network
// failures. They are not retried.
type localWriteError struct{ err error }

func (e *localWriteError) Error() string { return "write: " + e.err.Error() }
func (e *localWriteError) Unwrap() error { return e.err }

// trackingReader enforces maxBytes and remembers read-side errors so a
// failed copy can be attributed to the network or to the disk.
type trackingReader struct {
	r   io.Reader
	max int64
	n   int64
	err error
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.n += int64(n)
	if t.max > 0 && t.n > t.max {
		t.err = fmt.Errorf("%w: body > %d", ErrFileTooLarge, t.max)
		return n, t.err
	}
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}

// withRetries runs attempt up to 1+f.retries times. Network errors,
// stalled requests, HTTP 408, 429 and 5xx are retried with exponential
// back-off (honouring a short Retry-After); user cancellation, size-limit
// and local write errors are returned immediately.
func (f *Fetcher) withRetries(attempt func() (*FetchResult, time.Duration, error)) (*FetchResult, error) {
	var lastErr error
	var lastResp *FetchResult
	for i := 0; i <= f.retries; i++ {
		resp, retryAfter, err := attempt()
		if ctxErr := f.ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		var wErr *localWriteError
		switch {
		case err == nil && !retryableStatus(resp.Status):
			return resp, nil
		case err == nil:
			lastResp, lastErr = resp, nil
		case errors.Is(err, ErrFileTooLarge) || errors.As(err, &wErr):
			return nil, err
		default:
			lastResp, lastErr = nil, err
		}
		if i == f.retries {
			break
		}
		delay := f.backoff << i
		if retryAfter > delay {
			delay = min(retryAfter, maxRetryAfter)
		}
		select {
		case <-f.ctx.Done():
			return nil, f.ctx.Err()
		case <-time.After(delay):
		}
	}
	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func retryableStatus(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout
}

// openResponse is an in-flight response whose body is guarded by an idle
// timer: the request is aborted only when no data arrives for f.timeout, so
// large files on slow links are not killed by a total deadline.
type openResponse struct {
	result     *FetchResult
	body       io.ReadCloser
	retryAfter time.Duration
}

func (f *Fetcher) open(rawURL string, maxBytes int64) (*openResponse, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}

	reqCtx, reqCancel := context.WithCancel(f.ctx)
	idle := newIdleTimer(f.timeout, reqCancel)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		idle.stop()
		reqCancel()
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,pl;q=0.8")
	if f.cookie != "" && sameOrigin(parsed, f.cookieOrigin) {
		req.Header.Set("Cookie", f.cookie)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		idle.stop()
		reqCancel()
		return nil, idle.wrap(err)
	}
	idle.touch()
	body := &idleBody{rc: resp.Body, r: resp.Body, idle: idle, cancel: reqCancel}

	if maxBytes > 0 && resp.ContentLength > maxBytes {
		body.Close()
		return nil, fmt.Errorf("%w: content-length %d > %d", ErrFileTooLarge, resp.ContentLength, maxBytes)
	}
	if maxBytes > 0 {
		body.r = io.LimitReader(resp.Body, maxBytes+1)
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &openResponse{
		result: &FetchResult{
			OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
			Status:      resp.StatusCode,
			FinalURL:    finalURL,
			ContentType: resp.Header.Get("Content-Type"),
		},
		body:       body,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}, nil
}

// idleTimer cancels a request when it is not touched for timeout.
type idleTimer struct {
	t        *time.Timer
	timeout  time.Duration
	timedOut atomic.Bool
}

func newIdleTimer(timeout time.Duration, cancel context.CancelFunc) *idleTimer {
	it := &idleTimer{timeout: timeout}
	it.t = time.AfterFunc(timeout, func() {
		it.timedOut.Store(true)
		cancel()
	})
	return it
}

func (it *idleTimer) touch() { it.t.Reset(it.timeout) }
func (it *idleTimer) stop()  { it.t.Stop() }

func (it *idleTimer) wrap(err error) error {
	if err != nil && it.timedOut.Load() {
		return fmt.Errorf("%w (%s): %v", ErrIdleTimeout, it.timeout, err)
	}
	return err
}

type idleBody struct {
	rc     io.ReadCloser
	r      io.Reader
	idle   *idleTimer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.idle.touch()
	}
	if err != nil && err != io.EOF {
		err = b.idle.wrap(err)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.idle.stop()
	err := b.rc.Close()
	b.cancel()
	return err
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// IsHTML reports whether the response is HTML (or XHTML) based on Content-Type.
func (r *FetchResult) IsHTML() bool {
	ct := strings.ToLower(r.ContentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml")
}
