package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteFolderRemovesRecordedDownloadDirectly(t *testing.T) {
	previousStorage := appStorage
	previousTrash := trashFolder
	var trashed []string
	trashFolder = func(p string) error { trashed = append(trashed, p); return os.RemoveAll(p) }
	t.Cleanup(func() { appStorage = previousStorage; trashFolder = previousTrash })

	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "example.com")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "download-report.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	appStorage.prefsFile = filepath.Join(baseDir, "prefs.json")
	prefs := map[string]any{
		"history": []any{
			map[string]any{"id": "1", "output": target},
		},
	}
	data, err := jsonMarshalPrefs(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrefsBytes(data); err != nil {
		t.Fatal(err)
	}

	busy := &api{}
	busy.setActiveDownload("run", func() {}, nil, target)
	if result := busy.deleteFolder(target); result != "error: busy" {
		t.Fatalf("deleteFolder() of a running download = %q, want refusal", result)
	}

	if result := (&api{}).deleteFolder(target); result != "ok" {
		t.Fatalf("deleteFolder() = %q", result)
	}
	if len(trashed) != 1 || trashed[0] != target {
		t.Fatalf("folder must go through the Recycle Bin, trashed = %v", trashed)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists or returned an unexpected error: %v", err)
	}
	if _, err := os.Stat(baseDir); err != nil {
		t.Fatalf("parent directory should remain: %v", err)
	}
}

func TestValidateDeleteTargetRejectsUnrecordedAndRootPaths(t *testing.T) {
	unrecorded := filepath.Join(t.TempDir(), "not-in-history")
	if err := os.Mkdir(unrecorded, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := validateDeleteTarget(unrecorded, map[string]any{}); err == nil {
		t.Fatal("validateDeleteTarget() accepted an unrecorded directory")
	}

	root := filepath.VolumeName(unrecorded) + string(os.PathSeparator)
	if _, err := validateDeleteTarget(root, map[string]any{
		"history": []any{map[string]any{"output": root}},
	}); err == nil {
		t.Fatal("validateDeleteTarget() accepted a filesystem root")
	}
}
