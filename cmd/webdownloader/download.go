package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asrock/webdownloader/internal/downloader"
)

// downloadRequest is the JSON payload received from the frontend.
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
	// Engine picks the browser for mode "browser": "" (auto), "webview2",
	// "edge" or "chrome".
	Engine string `json:"engine,omitempty"`
	// Existing decides what happens when <site> already holds a download:
	// "update" (default) refreshes it in place, "new" creates <site>_vN.
	Existing string `json:"existing,omitempty"`
	// ResumeOutput continues a run from history in its existing folder
	// (skipping files already there) instead of creating <site>_vN.
	ResumeOutput string `json:"resumeOutput,omitempty"`
}

// downloadResult is returned immediately after a download starts.
type downloadResult struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	// StartFile is where the start page will be saved ("Open page").
	StartFile string `json:"startFile,omitempty"`
	// Updating is true when an existing copy is being refreshed in place.
	Updating bool   `json:"updating,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (a *api) download(requestJSON string) downloadResult {
	req, err := parseDownloadRequest(requestJSON)
	if err != nil {
		return downloadResult{Error: err.Error()}
	}

	parsedURL, err := validateDownloadURL(req.URL)
	if err != nil {
		return downloadResult{Error: err.Error()}
	}
	if req.Depth < 1 {
		req.Depth = 1
	}

	outputBase := strings.TrimSpace(req.OutputDir)
	if outputBase == "" {
		outputBase = a.defaultOutputPath("")
	}
	siteName := siteFolderName(parsedURL.Hostname())
	outputDir, updating := chooseOutputDir(outputBase, siteName, req.Existing)
	refresh := updating
	if strings.TrimSpace(req.ResumeOutput) != "" {
		resumeDir, err := validateResumeOutput(req.ResumeOutput, readPrefsSnapshot())
		if err != nil {
			return downloadResult{Error: "cannot resume: " + err.Error()}
		}
		outputDir = resumeDir
		refresh = false // resume keeps what is already there
	}
	if a.isActiveOutput(outputDir) {
		return downloadResult{Error: "busy: this folder is being downloaded right now"}
	}

	mode := downloader.ModeHTTP
	if strings.EqualFold(req.Mode, "browser") {
		mode = downloader.ModeBrowser
	}

	id := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), runSequence.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
	pause := downloader.NewPauseController()
	a.setActiveDownload(id, cancel, pause, outputDir)

	go a.runDownload(id, ctx, req, mode, pause, outputDir, refresh)

	startFile, _ := downloader.FilePathFor(outputDir, req.URL)
	return downloadResult{ID: id, Output: outputDir, StartFile: startFile, Updating: refresh}
}

// chooseOutputDir picks <base>/<site>. When that already holds a previous
// download, "update" (the default) reuses it and "new" picks <site>_vN.
func chooseOutputDir(baseDir, siteName, existing string) (dir string, updating bool) {
	dir = filepath.Join(baseDir, siteName)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return dir, false
	}
	if existing != "new" && looksLikeDownloadDir(dir) {
		return dir, true
	}
	return nextAvailableOutputDir(baseDir, siteName), false
}

func (a *api) runDownload(
	id string,
	ctx context.Context,
	req downloadRequest,
	mode downloader.Mode,
	pause *downloader.PauseController,
	outputDir string,
	refresh bool,
) {
	defer a.clearActiveDownload(id)

	_, _ = downloader.Download(downloader.Options{
		Context:               ctx,
		Pause:                 pause,
		URL:                   req.URL,
		OutputDir:             outputDir,
		MaxDepth:              req.Depth,
		Mode:                  mode,
		Engine:                downloader.Engine(strings.ToLower(strings.TrimSpace(req.Engine))),
		DownloadAll:           req.DownloadAll,
		IncludeSubdomains:     req.IncludeSubdomains,
		IncludeExternalAssets: req.IncludeExternal,
		MaxPages:              req.MaxPages,
		MaxTotalBytes:         megabytes(req.MaxTotalMB),
		MaxFileBytes:          megabytes(req.MaxFileMB),
		Retries:               2,
		Cookie:                req.Cookie,
		SkipExisting:          !refresh,
		Refresh:               refresh,
	}, downloader.Events{
		OnPage: func(event downloader.PageEvent) {
			a.fire("progress", id, event)
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
		OnComplete: func(summary downloader.Summary) {
			a.fire("complete", id, summary)
		},
	})
}

func (a *api) setActiveDownload(
	id string,
	cancel context.CancelFunc,
	pause *downloader.PauseController,
	outputDir string,
) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.activeRun != nil {
		a.activeRun()
	}
	a.activeRun = cancel
	a.activeRunID = id
	a.activePause = pause
	if a.running == nil {
		a.running = map[string]string{}
	}
	a.running[id] = outputDir
}

// isActiveOutput reports whether dir is the folder of the running download.
func (a *api) isActiveOutput(dir string) bool {
	if dir == "" {
		return false
	}
	left, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, active := range a.running {
		if right, err := filepath.Abs(active); err == nil && samePath(filepath.Clean(left), filepath.Clean(right)) {
			return true
		}
	}
	return false
}

func (a *api) clearActiveDownload(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// The folder stays busy until the goroutine has really finished, even
	// after Cancel cleared the active run.
	delete(a.running, id)
	if a.activeRunID != id {
		return
	}
	a.activeRun = nil
	a.activeRunID = ""
	a.activePause = nil
}

func (a *api) cancel(runID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.activeRun == nil || (runID != "" && runID != a.activeRunID) {
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

func parseDownloadRequest(value string) (downloadRequest, error) {
	var request downloadRequest
	value = strings.TrimSpace(value)
	if value == "" {
		return request, errors.New("empty request")
	}
	if err := json.Unmarshal([]byte(value), &request); err != nil {
		return request, fmt.Errorf("invalid request: %w", err)
	}
	return request, nil
}

func validateDownloadURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, errors.New("empty url")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, errors.New("invalid url")
	}
	if _, err := downloader.FilePathFor(".", rawURL); err != nil {
		return nil, fmt.Errorf("invalid url: %v", err)
	}
	return parsed, nil
}

var runSequence atomic.Uint64

// validateResumeOutput accepts only an existing download folder that is
// recorded in history.
func validateResumeOutput(dir string, prefs map[string]any) (string, error) {
	target, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	if !historyContainsOutput(prefs, target) {
		return "", errors.New("folder is not a download recorded in history")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() || !looksLikeDownloadDir(target) {
		return "", errors.New("previous download folder is missing")
	}
	return target, nil
}

// siteFolderName turns a URL hostname into a safe single folder name:
// no traversal (".."), nothing Windows rejects (IPv6 colons, reserved
// device names, trailing dots).
func siteFolderName(hostname string) string {
	name := strings.TrimPrefix(strings.ToLower(hostname), "www.")
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if strings.Trim(name, ".") == "" {
		return "site"
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
		return "_" + name
	}
	return name
}

func nextAvailableOutputDir(baseDir, siteName string) string {
	outputDir := filepath.Join(baseDir, siteName)
	if info, err := os.Stat(outputDir); err != nil || !info.IsDir() {
		return outputDir
	}

	for version := 2; version <= 999; version++ {
		candidate := filepath.Join(baseDir, fmt.Sprintf("%s_v%d", siteName, version))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return outputDir
}

func megabytes(value int) int64 {
	return int64(value) * 1024 * 1024
}
