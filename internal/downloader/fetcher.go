package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	maxRedirects   = 10
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// FetchResult is the result of a single HTTP fetch.
type FetchResult struct {
	OK          bool
	Status      int
	FinalURL    string
	Body        []byte
	ContentType string
}

// ErrFileTooLarge is returned before/while reading a response whose size
// exceeds the configured per-file limit.
var ErrFileTooLarge = errors.New("file exceeds configured size limit")

// Fetcher is a reusable HTTP client that follows redirects, has a UA header
// and enforces a per-request timeout. All in-flight requests can be cancelled
// at once via Cancel().
type Fetcher struct {
	client  *http.Client
	ctx     context.Context
	cancel  context.CancelFunc
	retries int
	cookie  string
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
	return &Fetcher{
		client: &http.Client{
			Timeout: defaultTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		ctx:     ctx,
		cancel:  cancel,
		retries: retries,
	}
}

func (f *Fetcher) SetCookie(cookie string) {
	f.cookie = strings.TrimSpace(cookie)
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
// It resolves redirects, applies a timeout, a desktop User-Agent header and
// is cancellable through Fetcher.Cancel.
func (f *Fetcher) FetchURL(rawURL string) (*FetchResult, error) {
	return f.FetchURLWithLimit(rawURL, 0)
}

// FetchURLWithLimit is like FetchURL, but enforces maxBytes before reading
// the full response into memory. maxBytes <= 0 means unlimited.
func (f *Fetcher) FetchURLWithLimit(rawURL string, maxBytes int64) (*FetchResult, error) {
	var lastErr error
	var lastResp *FetchResult
	for attempt := 0; attempt <= f.retries; attempt++ {
		resp, err := f.fetchOnce(rawURL, maxBytes)
		if err == nil {
			// Retry server-side transient errors; return all other responses.
			if resp.Status < 500 || attempt == f.retries {
				return resp, nil
			}
			lastResp = resp
		} else {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrFileTooLarge) {
				return nil, err
			}
			lastErr = err
			if attempt == f.retries {
				return nil, err
			}
		}
		select {
		case <-f.ctx.Done():
			return nil, f.ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
		}
	}
	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func (f *Fetcher) fetchOnce(rawURL string, maxBytes int64) (*FetchResult, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}

	// Per-request timeout layered on top of the (cancellable) parent context.
	reqCtx, reqCancel := context.WithTimeout(f.ctx, defaultTimeout)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,pl;q=0.8")
	if f.cookie != "" {
		req.Header.Set("Cookie", f.cookie)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: content-length %d > %d", ErrFileTooLarge, resp.ContentLength, maxBytes)
	}

	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: body > %d", ErrFileTooLarge, maxBytes)
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return &FetchResult{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:      resp.StatusCode,
		FinalURL:    finalURL,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// IsHTML reports whether the response is HTML (or XHTML) based on Content-Type.
func (r *FetchResult) IsHTML() bool {
	ct := strings.ToLower(r.ContentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml")
}
