package main

import (
	"context"
	"sync"

	webview "github.com/webview/webview_go"

	"github.com/asrock/webdownloader/internal/downloader"
	"github.com/asrock/webdownloader/internal/preview"
)

// api is the RPC layer exposed to the frontend.
type api struct {
	w  webview.WebView
	ui map[string]any
	mu sync.Mutex

	activeRun   context.CancelFunc
	activeRunID string
	activePause *downloader.PauseController
	// running maps run IDs to their output folders until each run's
	// goroutine ends: those folders must not be deleted or reused.
	running map[string]string

	previewMu sync.Mutex
	previews  map[string]*preview.Server
}

func newAPI(window webview.WebView, translations map[string]any) *api {
	return &api{
		w:  window,
		ui: translations,
	}
}
