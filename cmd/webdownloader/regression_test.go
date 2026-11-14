package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asrock/webdownloader/internal/downloader"
)

type firedEvent struct {
	name, id string
	payload  any
}

func recordingAPI() (*api, func() []firedEvent) {
	var mu sync.Mutex
	var events []firedEvent
	a := &api{onFire: func(event, id string, payload any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, firedEvent{event, id, payload})
	}}
	return a, func() []firedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]firedEvent(nil), events...)
	}
}

// H1: a run whose output folder cannot be created still tells the UI it
// finished (it used to stay in "Starting..." until Cancel).
func TestRunDownloadAlwaysFiresComplete(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, events := recordingAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.setActiveDownload("run1", cancel, downloader.NewPauseController(), filepath.Join(blocker, "out"))
	a.runDownload("run1", ctx, downloadRequest{URL: "https://example.com/", Depth: 1},
		downloader.ModeHTTP, downloader.NewPauseController(), filepath.Join(blocker, "out"), false)

	got := events()
	if len(got) != 1 || got[0].name != "complete" || got[0].id != "run1" {
		t.Fatalf("events = %+v, want one complete", got)
	}
	s, ok := got[0].payload.(completePayload)
	if !ok || !s.Failed || s.FailKind != "write" {
		t.Fatalf("complete payload = %#v", got[0].payload)
	}
	if a.isActiveOutput(filepath.Join(blocker, "out")) {
		t.Error("folder still marked busy")
	}
}

// M5: savePrefs returns at once even while the prefs file is busy, and the
// queued updates land in order.
func TestSavePrefsDoesNotBlockTheUIThread(t *testing.T) {
	previous := appStorage
	t.Cleanup(func() { flushPrefs(); appStorage = previous })
	appStorage.prefsFile = filepath.Join(t.TempDir(), "prefs.json")

	prefsMu.Lock() // a slow write in progress
	done := make(chan string, 2)
	go func() {
		done <- (&api{}).savePrefs(`{"a":1,"keep":true}`)
		done <- (&api{}).savePrefs(`{"a":2,"b":3}`)
	}()
	for i := 0; i < 2; i++ {
		select {
		case r := <-done:
			if r != "ok" {
				t.Errorf("savePrefs = %q", r)
			}
		case <-time.After(2 * time.Second):
			prefsMu.Unlock()
			t.Fatal("savePrefs blocked on the prefs lock")
		}
	}
	prefsMu.Unlock()

	var prefs map[string]any
	if err := json.Unmarshal([]byte((&api{}).loadPrefs()), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs["a"] != float64(2) || prefs["b"] != float64(3) || prefs["keep"] != true {
		t.Fatalf("prefs = %v", prefs)
	}
	if got := (&api{}).savePrefs(`{bad`); got == "ok" {
		t.Error("invalid JSON accepted")
	}
}

// M5: folder sizes are computed off the UI thread and delivered as an event.
func TestFolderInfoAsyncAnswersWithEvent(t *testing.T) {
	previous := appStorage
	t.Cleanup(func() { appStorage = previous })
	base := t.TempDir()
	target := filepath.Join(base, "example.com")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "download-report.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	appStorage.prefsFile = filepath.Join(base, "prefs.json")
	data, _ := jsonMarshalPrefs(map[string]any{"history": []any{map[string]any{"id": "1", "output": target}}})
	if err := writePrefsBytes(data); err != nil {
		t.Fatal(err)
	}

	a, events := recordingAPI()
	if id := a.folderInfoAsync("req7", target); id != "req7" {
		t.Fatalf("returned %q", id)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := events()
	if len(got) != 1 || got[0].name != "folderInfo" || got[0].id != "req7" {
		t.Fatalf("events = %+v", got)
	}
	info, ok := got[0].payload.(folderInfo)
	if !ok || !info.Exists || info.Files != 1 || info.Bytes != 2 {
		t.Fatalf("payload = %#v", got[0].payload)
	}
}
