package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asrock/webdownloader/internal/preview"
)

// previewResult is returned to the UI.
type previewResult struct {
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// startPreview serves a download folder from history on 127.0.0.1 (a random
// free port) and opens startFile in the default browser. One server per
// folder is kept until the app exits.
func (a *api) startPreview(outputDir, startFile string) previewResult {
	target, err := validateResumeOutput(outputDir, readPrefsSnapshot())
	if err != nil {
		return previewResult{Error: err.Error()}
	}
	key := strings.ToLower(target)

	a.previewMu.Lock()
	srv := a.previews[key]
	if srv == nil {
		srv, err = preview.Start(target)
		if err != nil {
			a.previewMu.Unlock()
			return previewResult{Error: err.Error()}
		}
		if a.previews == nil {
			a.previews = map[string]*preview.Server{}
		}
		a.previews[key] = srv
	}
	a.previewMu.Unlock()

	pageURL := srv.URL
	if startFile != "" {
		if abs, err := filepath.Abs(startFile); err == nil {
			pageURL = srv.PageURL(abs)
		}
	}
	if err := openURL(pageURL); err != nil {
		return previewResult{URL: pageURL, Error: err.Error()}
	}
	return previewResult{URL: pageURL}
}

// closePreviews stops every preview server (on exit).
func (a *api) closePreviews() {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	for key, srv := range a.previews {
		_ = srv.Close()
		delete(a.previews, key)
	}
}

// openURL opens one of our own http://127.0.0.1 preview URLs.
func openURL(u string) error {
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		return fmt.Errorf("refusing to open %q", u)
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		command = exec.Command("open", u)
	default:
		command = exec.Command("xdg-open", u)
	}
	return command.Start()
}
