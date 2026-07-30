package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	webview "github.com/webview/webview_go"
)

func bindAPI(window webview.WebView, api *api) error {
	bindings := []struct {
		name    string
		handler any
	}{
		{"api_download", api.download},
		{"api_cancel", api.cancel},
		{"api_pause", api.pause},
		{"api_resume", api.resume},
		{"api_pickFolder", api.pickFolder},
		{"api_openFolder", api.openFolder},
		{"api_openFile", api.openFile},
		{"api_revealPath", api.revealPath},
		{"api_getLocale", api.getLocale},
		{"api_defaultOutputPath", api.defaultOutputPath},
		{"api_i18n", api.i18n},
		{"api_savePrefs", api.savePrefs},
		{"api_loadPrefs", api.loadPrefs},
		{"api_deleteFolder", api.deleteFolder},
		{"api_deleteHistoryItem", api.deleteHistoryItem},
	}

	for _, binding := range bindings {
		if err := window.Bind(binding.name, binding.handler); err != nil {
			return fmt.Errorf("bind frontend function %q: %w", binding.name, err)
		}
	}
	return nil
}

func (a *api) fire(event, id string, payload any) {
	if event == "" {
		return
	}

	wrapped := map[string]any{
		"id":      id,
		"payload": payload,
	}
	data, err := json.Marshal(wrapped)
	if err != nil {
		log.Printf("marshal event %q: %v", event, err)
		return
	}

	a.w.Dispatch(func() {
		functionName := "_fire" + strings.ToUpper(event[:1]) + event[1:]
		script := fmt.Sprintf(
			"if (window.%s) window.%s(%s);",
			functionName,
			functionName,
			string(data),
		)
		a.w.Eval(script)
	})
}

// writeHTMLToTemp extracts the embedded frontend to a file URL so WebView2
// can use localStorage and other origin-bound browser APIs.
func writeHTMLToTemp(html []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), "webdownloader")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
