package main

import (
	"encoding/json"
	"errors"
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
		// Keep the unreadable file instead of letting the next save
		// silently replace it (and the whole history) with defaults.
		_ = os.Rename(prefsPath(), prefsPath()+".corrupt")
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

// writePrefsBytes replaces prefs.json atomically so a crash or a full disk
// cannot leave a truncated file behind.
func writePrefsBytes(data []byte) error {
	target := prefsPath()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".prefs-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
