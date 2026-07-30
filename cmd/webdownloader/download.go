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
}

// downloadResult is returned immediately after a download starts.
type downloadResult struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
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
	siteName := strings.TrimPrefix(parsedURL.Hostname(), "www.")
	outputDir := nextAvailableOutputDir(outputBase, siteName)

	mode := downloader.ModeHTTP
	if strings.EqualFold(req.Mode, "browser") {
		mode = downloader.ModeBrowser
	}

	id := fmt.Sprintf("%d", time.Now().UnixMilli())
	ctx, cancel := context.WithCancel(context.Background())
	pause := downloader.NewPauseController()
	a.setActiveDownload(id, cancel, pause)

	go a.runDownload(id, ctx, req, mode, pause, outputDir)

	return downloadResult{ID: id, Output: outputDir}
}

func (a *api) runDownload(
	id string,
	ctx context.Context,
	req downloadRequest,
	mode downloader.Mode,
	pause *downloader.PauseController,
	outputDir string,
) {
	defer a.clearActiveDownload(id)

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
		MaxTotalBytes:         megabytes(req.MaxTotalMB),
		MaxFileBytes:          megabytes(req.MaxFileMB),
		Retries:               2,
		Cookie:                req.Cookie,
		SkipExisting:          true,
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
) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.activeRun != nil {
		a.activeRun()
	}
	a.activeRun = cancel
	a.activeRunID = id
	a.activePause = pause
}

func (a *api) clearActiveDownload(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid url")
	}
	return parsed, nil
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
