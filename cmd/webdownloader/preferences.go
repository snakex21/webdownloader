package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var prefsMu sync.Mutex

func (a *api) i18n() map[string]any {
	return a.ui
}

// savePrefs merges data into prefs.json. It runs on the UI thread (every
// few seconds while downloading, to keep history current), so it only
// parses the JSON and hands the write to a background goroutine; queued
// updates are applied in order and written once.
func (a *api) savePrefs(data string) string {
	incoming := map[string]any{}
	if strings.TrimSpace(data) != "" {
		if err := json.Unmarshal([]byte(data), &incoming); err != nil {
			return fmt.Sprintf("error: %v", err)
		}
	}
	prefsWriter.enqueue(incoming)
	return "ok"
}

func (a *api) loadPrefs() string {
	flushPrefs()
	prefsMu.Lock()
	defer prefsMu.Unlock()

	data, err := os.ReadFile(prefsPath())
	if err != nil {
		return ""
	}
	return string(data)
}

// prefsWriter applies savePrefs updates off the UI thread.
var prefsWriter = newPrefsQueue()

type prefsQueue struct {
	mu      sync.Mutex
	idle    *sync.Cond
	pending []map[string]any
	running bool
}

func newPrefsQueue() *prefsQueue {
	q := &prefsQueue{}
	q.idle = sync.NewCond(&q.mu)
	return q
}

func (q *prefsQueue) enqueue(update map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, update)
	if !q.running {
		q.running = true
		go q.drain()
	}
}

func (q *prefsQueue) drain() {
	for {
		q.mu.Lock()
		batch := q.pending
		q.pending = nil
		if len(batch) == 0 {
			q.running = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
		if err := applyPrefs(batch); err != nil {
			log.Printf("save preferences: %v", err)
		}
	}
}

// wait blocks until every queued update is on disk.
func (q *prefsQueue) wait() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.running {
		q.idle.Wait()
	}
}

// flushPrefs waits for queued savePrefs writes, so readers see them.
func flushPrefs() { prefsWriter.wait() }

func applyPrefs(updates []map[string]any) error {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	prefs := readPrefsMap()
	for _, update := range updates {
		for key, value := range update {
			prefs[key] = value
		}
	}
	output, err := jsonMarshalPrefs(prefs)
	if err != nil {
		return err
	}
	return writePrefsBytes(output)
}

func (a *api) deleteHistoryItem(id string) string {
	flushPrefs()
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
	flushPrefs()
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
