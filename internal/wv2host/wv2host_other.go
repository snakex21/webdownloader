//go:build !windows

// Package wv2host is only implemented on Windows, where WebView2 exists.
package wv2host

import (
	"errors"
	"time"
)

// ErrRuntimeNotFound means no WebView2 runtime is installed.
var ErrRuntimeNotFound = errors.New("WebView2 runtime not found")

// Host is a running hidden WebView2 browser (Windows only).
type Host struct {
	DevToolsURL string
	Version     string
}

// Available reports whether a WebView2 runtime is installed.
func Available() bool { return false }

// Start is unsupported outside Windows.
func Start(time.Duration) (*Host, error) { return nil, ErrRuntimeNotFound }

// Close is a no-op outside Windows.
func (h *Host) Close() {}
