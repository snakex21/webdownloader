package main

import (
	"context"
	"sync"

	webview "github.com/webview/webview_go"

	"github.com/asrock/webdownloader/internal/downloader"
)

// api is the RPC layer exposed to the frontend.
type api struct {
	w  webview.WebView
	ui map[string]any
	mu sync.Mutex

	activeRun   context.CancelFunc
	activeRunID string
	activePause *downloader.PauseController
}

func newAPI(window webview.WebView, translations map[string]any) *api {
	return &api{
		w:  window,
		ui: translations,
	}
}
