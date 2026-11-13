package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var prefsMu sync.Mutex

func (a *api) i18n() map[string]any {
	return a.ui
}

func (a *api) savePrefs(data string) string {
	prefsMu.Lock()
	defer prefsMu.Unlock()

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

	output, err := jsonMarshalPrefs(prefs)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if err := writePrefsBytes(output); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

func (a *api) loadPrefs() string {
	prefsMu.Lock()
	defer prefsMu.Unlock()

	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return ""
	}
	return string(data)
}

func (a *api) deleteHistoryItem(id string) string {
	prefsMu.Lock()
	defer prefsMu.Unlock()

	prefs := readPrefsMap()
	rawHistory, ok := prefs["history"].([]any)
	if !ok {
		return "ok"
	}

	history := make([]any, 0, len(rawHistory))
	for _, item := range rawHistory {
		entry, ok := item.(map[string]any)
		if ok && entry["id"] == id {
			continue
		}
		history = append(history, item)
	}

	prefs["history"] = history
	if data, err := jsonMarshalPrefs(prefs); err == nil {
		_ = writePrefsBytes(data)
	}
	return "ok"
}

func prefsPath() string {
	if appStorage.prefsFile != "" {
		return appStorage.prefsFile
	}
	executableDir, err := executableDirectory()
	if err != nil {
		return "prefs.json"
	}
	return filepath.Join(executableDir, "prefs.json")
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

func readPrefsSnapshot() map[string]any {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	return readPrefsMap()
}

func jsonMarshalPrefs(prefs map[string]any) ([]byte, error) {
	return json.MarshalIndent(prefs, "", "  ")
}

func writePrefsBytes(data []byte) error {
	return os.WriteFile(prefsPath(), data, 0o644)
}
