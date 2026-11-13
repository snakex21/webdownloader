package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asrock/webdownloader/internal/downloader"
	"github.com/asrock/webdownloader/internal/locale"
)

func (a *api) pickFolder() string {
	if runtime.GOOS == "windows" {
		path, _ := pickFolderWindows(uintptr(a.w.Window()))
		// "" means the dialog was cancelled; the UI keeps the current folder.
		return path
	}
	return ""
}

func (a *api) openFolder(path string) error {
	// explorer/open/xdg-open would *run* a file passed here, and the path
	// comes from frontend-controlled history, so only accept directories.
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %q", path)
	}

	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer", path)
	case "darwin":
		command = exec.Command("open", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	return command.Start()
}

// openableExtensions are the only files the UI needs to open (the saved
// index page and reports). Anything else (.exe, .lnk, .bat, URLs) is refused
// because the shell handler would execute it.
var openableExtensions = map[string]struct{}{
	".html": {}, ".htm": {}, ".json": {}, ".log": {}, ".txt": {},
}

func validateOpenFile(path string) error {
	if _, ok := openableExtensions[strings.ToLower(filepath.Ext(path))]; !ok {
		return fmt.Errorf("refusing to open %q", path)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %q", path)
	}
	return nil
}

func (a *api) openFile(path string) error {
	if err := validateOpenFile(path); err != nil {
		return err
	}

	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		command = exec.Command("open", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	return command.Start()
}

func (a *api) revealPath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}

	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer", "/select,"+path)
	case "darwin":
		command = exec.Command("open", "-R", path)
	default:
		command = exec.Command("xdg-open", filepath.Dir(path))
	}
	return command.Start()
}

// trashFolder is replaced in tests so they do not fill the Recycle Bin.
var trashFolder = moveToTrash

// deleteFolder moves a download folder recorded in history to the Recycle
// Bin. A folder that is being downloaded right now is refused.
func (a *api) deleteFolder(path string) string {
	target, err := validateDeleteTarget(path, readPrefsSnapshot())
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if a.isActiveOutput(target) {
		return "error: busy"
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return "ok"
	}
	if err := trashFolder(target); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// folderInfo is shown in the delete confirmation.
type folderInfo struct {
	Exists bool  `json:"exists"`
	Bytes  int64 `json:"bytes"`
	Files  int   `json:"files"`
	Busy   bool  `json:"busy"`
}

// folderInfo sizes a download folder recorded in history.
func (a *api) folderInfo(path string) folderInfo {
	target, err := validateDeleteTarget(path, readPrefsSnapshot())
	if err != nil {
		return folderInfo{}
	}
	info := folderInfo{Busy: a.isActiveOutput(target)}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		return info
	}
	info.Exists = true
	_ = filepath.WalkDir(target, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			info.Files++
			info.Bytes += fi.Size()
		}
		return nil
	})
	return info
}

func validateDeleteTarget(path string, prefs map[string]any) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}

	target, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	volumeRoot := filepath.VolumeName(target) + string(os.PathSeparator)
	if samePath(target, volumeRoot) {
		return "", fmt.Errorf("refusing to delete a filesystem root")
	}
	if !historyContainsOutput(prefs, target) {
		return "", fmt.Errorf("folder is not a download recorded in history")
	}

	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return target, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect folder: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("target is not a directory")
	}
	// History lives in a file the frontend can rewrite, so also require
	// the folder to look like something Download produced.
	if !looksLikeDownloadDir(target) && !isEmptyDir(target) {
		return "", fmt.Errorf("folder does not look like a WebDownloader output")
	}
	return target, nil
}

// downloadMarkers are files Download creates in every output folder
// (the error log is opened at the start of a run, the report at the end).
var downloadMarkers = []string{"download-errors.log", "download-report.json"}

func looksLikeDownloadDir(dir string) bool {
	for _, marker := range downloadMarkers {
		if info, err := os.Lstat(filepath.Join(dir, marker)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func historyContainsOutput(prefs map[string]any, target string) bool {
	history, ok := prefs["history"].([]any)
	if !ok {
		return false
	}
	for _, item := range history {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		output, ok := entry["output"].(string)
		if !ok || strings.TrimSpace(output) == "" {
			continue
		}
		absoluteOutput, err := filepath.Abs(filepath.Clean(output))
		if err == nil && samePath(target, absoluteOutput) {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (a *api) getLocale() string {
	return locale.Detect()
}

func (a *api) defaultOutputPath(rawURL string) string {
	baseDir := defaultOutputBase()

	if rawURL == "" {
		return baseDir
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return filepath.Join(baseDir, strings.TrimPrefix(parsed.Hostname(), "www."))
	}
	return baseDir
}

// engines reports which browser engines the accurate mode can use here.
func (a *api) engines() map[string]bool {
	return downloader.AvailableEngines()
}
