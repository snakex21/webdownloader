package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteFolderRemovesRecordedDownloadDirectly(t *testing.T) {
	previousStorage := appStorage
	t.Cleanup(func() { appStorage = previousStorage })

	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "example.com")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("test"), 0o644); err != nil {
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

	if result := (&api{}).deleteFolder(target); result != "ok" {
		t.Fatalf("deleteFolder() = %q", result)
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
