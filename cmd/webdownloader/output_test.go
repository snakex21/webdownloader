package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChooseOutputDir(t *testing.T) {
	base := t.TempDir()
	dir, updating := chooseOutputDir(base, "site", "")
	if dir != filepath.Join(base, "site") || updating {
		t.Fatalf("fresh: %s %v", dir, updating)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A folder that is not a previous download is never reused.
	if d, u := chooseOutputDir(base, "site", "update"); d != filepath.Join(base, "site_v2") || u {
		t.Fatalf("foreign folder: %s %v", d, u)
	}
	if err := os.WriteFile(filepath.Join(dir, "download-report.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, u := chooseOutputDir(base, "site", ""); d != dir || !u {
		t.Fatalf("default must update the existing copy: %s %v", d, u)
	}
	if d, u := chooseOutputDir(base, "site", "new"); d != filepath.Join(base, "site_v2") || u {
		t.Fatalf("new copy: %s %v", d, u)
	}
}

func TestIsActiveOutput(t *testing.T) {
	a := &api{}
	dir := t.TempDir()
	a.setActiveDownload("1", func() {}, nil, dir)
	if !a.isActiveOutput(dir + string(filepath.Separator)) {
		t.Fatal("running folder not reported as busy")
	}
	a.cancel("1")
	if !a.isActiveOutput(dir) {
		t.Fatal("folder must stay busy until the run goroutine finishes")
	}
	a.clearActiveDownload("1")
	if a.isActiveOutput(dir) {
		t.Fatal("folder still busy after the run finished")
	}
}

func TestCancelWatchdogUnblocksUI(t *testing.T) {
	var fired []string
	a := &api{onFire: func(event, id string, payload any) { fired = append(fired, event+":"+id) }}

	stuck := &runInfo{done: make(chan struct{})}
	a.cancelWatchdog("stuck", stuck, 20*time.Millisecond)
	if len(fired) != 1 || fired[0] != "complete:stuck" {
		t.Fatalf("stuck run: fired %v, want a forced complete", fired)
	}

	fired = nil
	finished := &runInfo{done: make(chan struct{})}
	close(finished.done)
	a.cancelWatchdog("ok", finished, 20*time.Millisecond)
	if len(fired) != 0 {
		t.Fatalf("finished run must not get a second complete: %v", fired)
	}
}

func TestCancelReportsAndStopsActiveRun(t *testing.T) {
	a := &api{onFire: func(string, string, any) {}}
	cancelled := false
	a.setActiveDownload("r1", func() { cancelled = true }, nil, t.TempDir())
	if got := a.cancel("other"); got != "" || cancelled {
		t.Fatalf("cancel of another id must be ignored (got %q)", got)
	}
	if got := a.cancel("r1"); got != "r1" || !cancelled {
		t.Fatalf("cancel(r1) = %q, cancelled=%v", got, cancelled)
	}
	a.clearActiveDownload("r1")
}
