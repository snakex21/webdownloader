// Command webdownloader is a desktop application that mirrors a website
// (HTML pages, assets and attachments) to a local directory.
//
// The UI lives in a single HTML file embedded in the binary; the
// application logic (HTTP fetching, HTML rewriting, BFS crawl) runs in
// Go and is exposed to the front-end through webview's RPC bindings.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	webview "github.com/webview/webview_go"

	"github.com/asrock/webdownloader/internal/downloader"
	"github.com/asrock/webdownloader/internal/locale"
)

//go:embed web/*
var webFS embed.FS

func main() {
	debug := flag.Bool("debug", false, "enable webview devtools")
	flag.Parse()

	htmlBytes, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("read embedded html: %v", err)
	}

	ui, err := loadTranslations()
	if err != nil {
		log.Printf("warn: load translations: %v (falling back to en)", err)
	}

	// Write the embedded HTML to a real file and navigate to it
	// (file:// URL) so localStorage works. A data: URI (used by SetHtml)
	// blocks localStorage and some other storage APIs in WebView2.
	htmlPath, err := writeHTMLToTemp(htmlBytes)
	if err != nil {
		log.Fatalf("write html: %v", err)
	}

	w := webview.New(*debug)
	defer w.Destroy()
	w.SetTitle("WebDownloader")
	windowWidth, windowHeight := loadWindowSize()
	w.SetSize(windowWidth, windowHeight, webview.HintNone)
	w.SetSize(900, 720, webview.HintMin)

	api := newAPI(w, ui)
	w.Bind("api_download", api.download)
	w.Bind("api_cancel", api.cancel)
	w.Bind("api_pause", api.pause)
	w.Bind("api_resume", api.resume)
	w.Bind("api_pickFolder", api.pickFolder)
	w.Bind("api_openFolder", api.openFolder)
	w.Bind("api_openFile", api.openFile)
	w.Bind("api_revealPath", api.revealPath)
	w.Bind("api_getLocale", api.getLocale)
	w.Bind("api_defaultOutputPath", api.defaultOutputPath)
	w.Bind("api_i18n", api.i18n)
	w.Bind("api_savePrefs", api.savePrefs)
	w.Bind("api_loadPrefs", api.loadPrefs)
	w.Bind("api_deleteFolder", api.deleteFolder)
	w.Bind("api_deleteHistoryItem", api.deleteHistoryItem)

	w.Init("window.__goReady = true;")
	w.Navigate("file:///" + filepath.ToSlash(htmlPath))
	w.Run()
	saveWindowSize(uintptr(w.Window()))
}

// -----------------------------------------------------------------------------
// API
// -----------------------------------------------------------------------------

// api is the RPC layer exposed to the front-end. Every public method has a
// matching webview binding in main(). Methods that take user input accept
// arguments positionally as JSON-decoded values.
type api struct {
	w  webview.WebView
	ui map[string]any
	mu sync.Mutex

	// activeRun is the cancel function of the currently running download
	// (if any). Calling it aborts every in-flight HTTP request and
	// stops the BFS loop on the next tick.
	activeRun   context.CancelFunc
	activeRunID string
	activePause *downloader.PauseController
}

func newAPI(w webview.WebView, ui map[string]any) *api {
	return &api{w: w, ui: ui}
}

// downloadRequest is the JSON payload the front-end sends to api_download.
// We unmarshal it from a single string argument so adding new fields
// doesn't break older builds.
type downloadRequest struct {
	URL               string `json:"url"`
	OutputDir         string `json:"outputDir"`
	Depth             int    `json:"depth"`
	DownloadAll       bool   `json:"downloadAll"`
	Mode              string `json:"mode"`
	IncludeSubdomains bool   `json:"includeSubdomains"`
	IncludeExternal   bool   `json:"includeExternal"`
	MaxPages          int    `json:"maxPages"`
	MaxTotalMB        int    `json:"maxTotalMB"`
	MaxFileMB         int    `json:"maxFileMB"`
	Cookie            string `json:"cookie"`
}

// downloadResult is what JS receives from api_download().
type downloadResult struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// download starts a recursive download in a background goroutine and
// returns the assigned id immediately. Progress is reported via
// window._fireProgress / _fireAsset / _fireError / _fireComplete.
func (a *api) download(requestJSON string) downloadResult {
	req, err := parseDownloadRequest(requestJSON)
	if err != nil {
		return downloadResult{Error: err.Error()}
	}
	if strings.TrimSpace(req.URL) == "" {
		return downloadResult{Error: "empty url"}
	}
	parsed, perr := url.Parse(req.URL)
	if perr != nil || parsed.Scheme == "" || parsed.Host == "" {
		return downloadResult{Error: "invalid url"}
	}
	if req.Depth < 1 {
		req.Depth = 1
	}

	outputBase := strings.TrimSpace(req.OutputDir)
	if outputBase == "" {
		outputBase = a.defaultOutputPath(req.URL)
	}
	siteName := strings.TrimPrefix(parsed.Hostname(), "www.")
	outputDir := filepath.Join(outputBase, siteName)

	// Versioning: if the output directory exists, find the next available
	// version number (e.g., sitename_v2, sitename_v3, ...).
	if fi, stErr := os.Stat(outputDir); stErr == nil && fi.IsDir() {
		for v := 2; v <= 999; v++ {
			versioned := filepath.Join(outputBase, siteName+"_v"+fmt.Sprintf("%d", v))
			if _, err := os.Stat(versioned); os.IsNotExist(err) {
				outputDir = versioned
				break
			}
		}
	}

	mode := downloader.ModeHTTP
	if strings.EqualFold(req.Mode, "browser") {
		mode = downloader.ModeBrowser
	}

	id := fmt.Sprintf("%d", time.Now().UnixMilli())

	ctx, cancel := context.WithCancel(context.Background())
	pause := downloader.NewPauseController()
	a.mu.Lock()
	// If a previous run is still in flight, cancel it before starting
	// a new one — the user can only really run one crawl at a time in
	// the UI anyway.
	if a.activeRun != nil {
		a.activeRun()
	}
	a.activeRun = cancel
	a.activeRunID = id
	a.activePause = pause
	a.mu.Unlock()

	go func() {
		_, _ = downloader.Download(downloader.Options{
			Context:               ctx,
			Pause:                 pause,
			URL:                   req.URL,
			OutputDir:             outputDir,
			MaxDepth:              req.Depth,
			Mode:                  mode,
			DownloadAll:           req.DownloadAll,
			IncludeSubdomains:     req.IncludeSubdomains,
			IncludeExternalAssets: req.IncludeExternal,
			MaxPages:              req.MaxPages,
			MaxTotalBytes:         int64(req.MaxTotalMB) * 1024 * 1024,
			MaxFileBytes:          int64(req.MaxFileMB) * 1024 * 1024,
			Retries:               2,
			Cookie:                req.Cookie,
			SkipExisting:          true,
		}, downloader.Events{
			OnPage: func(ev downloader.PageEvent) {
				a.fire("progress", id, ev)
			},
			OnAsset: func(assetURL, kind string) {
				a.fire("asset", id, map[string]string{"url": assetURL, "kind": kind})
			},
			OnError: func(pageURL string, err error) {
				a.fire("error", id, map[string]string{
					"url":   pageURL,
					"error": err.Error(),
				})
			},
			OnComplete: func(s downloader.Summary) {
				a.fire("complete", id, s)
			},
		})
		// Clear the active run reference now that this run is done.
		a.mu.Lock()
		if a.activeRunID == id {
			a.activeRun = nil
			a.activeRunID = ""
			a.activePause = nil
		}
		a.mu.Unlock()
	}()

	return downloadResult{ID: id, Output: outputDir}
}

// cancel aborts the in-flight download (if any) and reports back the id
// of the run that was cancelled. Returns "" when nothing was running.
func (a *api) cancel(runID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun == nil {
		return ""
	}
	// The front-end only ever runs one download at a time, so we
	// accept the id if it matches OR is empty (force-cancel).
	if runID != "" && runID != a.activeRunID {
		return ""
	}
	a.activeRun()
	id := a.activeRunID
	a.activeRun = nil
	a.activeRunID = ""
	a.activePause = nil
	return id
}

func (a *api) pause(runID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activePause == nil || (runID != "" && runID != a.activeRunID) {
		return ""
	}
	a.activePause.Pause()
	return a.activeRunID
}

func (a *api) resume(runID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activePause == nil || (runID != "" && runID != a.activeRunID) {
		return ""
	}
	a.activePause.Resume()
	return a.activeRunID
}

func parseDownloadRequest(s string) (downloadRequest, error) {
	var r downloadRequest
	s = strings.TrimSpace(s)
	if s == "" {
		return r, errors.New("empty request")
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return r, fmt.Errorf("invalid request: %w", err)
	}
	return r, nil
}

// pickFolder opens a native folder picker. The implementation differs by
// platform; on Windows it shells out to PowerShell + FolderBrowserDialog
// (no need for a separate build tag).
func (a *api) pickFolder() string {
	switch runtime.GOOS {
	case "windows":
		// a.w.Window() returns the HWND of the WebView2 host window;
		// the dialog opens modal to that so it tracks the main window.
		if p, err := pickFolderWindows(uintptr(a.w.Window())); err == nil && p != "" {
			return p
		}
	}
	return a.defaultOutputPath("")
}

// openFolder opens the given path in the OS file explorer.
func (a *api) openFolder(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// openFile opens the given file with the OS default application.
func (a *api) openFile(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// revealPath opens the file manager and selects the given file when possible.
func (a *api) revealPath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", "/select,"+path)
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	}
	return cmd.Start()
}

// getLocale returns the system UI language (two-letter code).
func (a *api) deleteFolder(path string) string {
	if path == "" {
		return "empty path"
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	// Also clean up parent directory if it's now empty
	parent := filepath.Dir(path)
	if entries, _ := os.ReadDir(parent); len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return "ok"
}

func (a *api) deleteHistoryItem(id string) string {
	prefs := readPrefsMap()
	if raw, ok := prefs["history"]; ok {
		if arr, ok := raw.([]any); ok {
			var out []any
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if m["id"] == id {
						continue
					}
				}
				out = append(out, item)
			}
			prefs["history"] = out
			if data, err := jsonMarshalPrefs(prefs); err == nil {
				_ = writePrefsBytes(data)
			}
		}
	}
	return "ok"
}

// getLocale returns the system UI language (two-letter code).
func (a *api) getLocale() string {
	return locale.Detect()
}

// defaultOutputPath returns the suggested output directory. When url is
// empty it returns "<exe-dir>/output"; otherwise it appends the hostname.
func (a *api) defaultOutputPath(rawURL string) string {
	exe, err := os.Executable()
	if err != nil {
		exe, _ = os.Getwd()
	}
	base := filepath.Join(filepath.Dir(exe), "output")
	if rawURL == "" {
		return base
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return filepath.Join(base, strings.TrimPrefix(parsed.Hostname(), "www."))
	}
	return base
}

// i18n returns the embedded translation table.
func (a *api) i18n() map[string]any {
	return a.ui
}

// prefsPath returns the path to prefs.json next to the executable.
func prefsPath() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "webdownloader"
	}
	return filepath.Join(filepath.Dir(exe), "prefs.json")
}

// savePrefs merges a JSON string into prefs.json next to the EXE.
func (a *api) savePrefs(data string) string {
	incoming := map[string]any{}
	if strings.TrimSpace(data) != "" {
		if err := json.Unmarshal([]byte(data), &incoming); err != nil {
			return fmt.Sprintf("error: %v", err)
		}
	}

	prefs := readPrefsMap()
	for key, value := range incoming {
		prefs[key] = value
	}
	out, err := jsonMarshalPrefs(prefs)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if err := writePrefsBytes(out); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// loadPrefs reads prefs.json next to the EXE. Returns empty string if
// the file doesn't exist (first run).
func (a *api) loadPrefs() string {
	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return ""
	}
	return string(data)
}

func readPrefsMap() map[string]any {
	prefs := map[string]any{}
	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return prefs
	}
	if err := json.Unmarshal(data, &prefs); err != nil {
		return map[string]any{}
	}
	return prefs
}

func jsonMarshalPrefs(prefs map[string]any) ([]byte, error) {
	return json.MarshalIndent(prefs, "", "  ")
}

func writePrefsBytes(data []byte) error {
	return os.WriteFile(prefsPath(), data, 0644)
}

// fire pushes a payload to the front-end through window._fire<EventName>.
// All events are JSON-encoded and dispatched on the UI thread.
func (a *api) fire(event, id string, payload any) {
	wrapped := map[string]any{
		"id":      id,
		"payload": payload,
	}
	js, err := json.Marshal(wrapped)
	if err != nil {
		log.Printf("marshal event %q: %v", event, err)
		return
	}
	a.w.Dispatch(func() {
		fn := "_fire" + strings.ToUpper(event[:1]) + event[1:]
		// We can't use template literals reliably inside Eval, so we embed
		// the JSON via a hidden element. Cheapest path: build a JS literal
		// and use JSON.parse on the back end.
		script := fmt.Sprintf("if (window.%s) window.%s(%s);", fn, fn, string(js))
		a.w.Eval(script)
	})
}

func loadTranslations() (map[string]any, error) {
	data, err := webFS.ReadFile("web/i18n.json")
	if err != nil {
		return nil, err
	}
	out := make(map[string]any)
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// writeHTMLToTemp extracts the embedded web/index.html to a real file
// in %TEMP%\webdownloader\ and returns its path. We use a file:// URL
// (not data:) so localStorage and other storage APIs work in WebView2.
func writeHTMLToTemp(htmlBytes []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), "webdownloader")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, htmlBytes, 0644); err != nil {
		return "", err
	}
	return path, nil
}
