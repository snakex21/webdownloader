package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteFolderNameIsASafeSingleComponent(t *testing.T) {
	cases := map[string]string{
		"www.Example.com": "example.com",
		"..":              "site",
		".":               "site",
		"":                "site",
		"::1":             "__1",
		"fe80::1%eth0":    "fe80__1%eth0",
		"con":             "_con",
		"lpt1.local":      "_lpt1.local",
		"host.":           "host",
	}
	for in, want := range cases {
		if got := siteFolderName(in); got != want {
			t.Errorf("siteFolderName(%q) = %q, want %q", in, got, want)
		}
	}
	base := t.TempDir()
	for in := range cases {
		dir := nextAvailableOutputDir(base, siteFolderName(in))
		if filepath.Dir(dir) != base {
			t.Errorf("host %q escapes the output base: %q", in, dir)
		}
	}
}

func TestValidateOpenFileRejectsExecutables(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"index.html", "report.json", "evil.exe", "evil.lnk", "evil.bat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index.html", "report.json"} {
		if err := validateOpenFile(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	for _, p := range []string{
		filepath.Join(dir, "evil.exe"), filepath.Join(dir, "evil.lnk"), filepath.Join(dir, "evil.bat"),
		filepath.Join(dir, "missing.html"), "https://example.com/x.html", "",
	} {
		if err := validateOpenFile(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestOpenFolderRejectsFiles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&api{}).openFolder(file); err == nil {
		t.Fatal("openFolder accepted a file (explorer would execute it)")
	}
	if err := (&api{}).openFolder(""); err == nil {
		t.Fatal("openFolder accepted an empty path")
	}
}

func TestValidateDeleteTargetRequiresDownloadFolder(t *testing.T) {
	userDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(userDir, "thesis.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A tampered history entry pointing at an unrelated folder.
	prefs := map[string]any{"history": []any{map[string]any{"output": userDir}}}
	if _, err := validateDeleteTarget(userDir, prefs); err == nil {
		t.Fatal("deleting a non-download folder was allowed")
	}
	if err := os.WriteFile(filepath.Join(userDir, "download-errors.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDeleteTarget(userDir, prefs); err != nil {
		t.Fatalf("download folder rejected: %v", err)
	}
}

func TestValidateResumeOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "example.com")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{"history": []any{map[string]any{"output": out}}}
	if _, err := validateResumeOutput(out, prefs); err == nil {
		t.Fatal("folder without download markers accepted")
	}
	if err := os.WriteFile(filepath.Join(out, "download-errors.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := validateResumeOutput(out, prefs)
	if err != nil || got != out {
		t.Fatalf("validateResumeOutput = %q, %v", got, err)
	}
	if _, err := validateResumeOutput(out, map[string]any{}); err == nil {
		t.Fatal("folder not in history accepted")
	}
}

func TestPrefsWriteIsAtomicAndCorruptFileIsKept(t *testing.T) {
	previous := appStorage
	t.Cleanup(func() { appStorage = previous })
	dir := t.TempDir()
	appStorage.prefsFile = filepath.Join(dir, "prefs.json")

	if err := writePrefsBytes([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	if err := os.WriteFile(appStorage.prefsFile, []byte(`{"history": [tru`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (&api{}).savePrefs(`{"lang":"pl"}`); got != "ok" {
		t.Fatalf("savePrefs = %q", got)
	}
	corrupt, err := os.ReadFile(appStorage.prefsFile + ".corrupt")
	if err != nil || !strings.Contains(string(corrupt), "history") {
		t.Fatalf("corrupt prefs not preserved: %q, %v", corrupt, err)
	}
}
