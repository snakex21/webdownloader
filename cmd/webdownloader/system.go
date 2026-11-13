package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asrock/webdownloader/internal/locale"
)

func (a *api) pickFolder() string {
	if runtime.GOOS == "windows" {
		if path, err := pickFolderWindows(uintptr(a.w.Window())); err == nil && path != "" {
			return path
		}
	}
	return a.defaultOutputPath("")
}

func (a *api) openFolder(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
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

func (a *api) openFile(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
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

func (a *api) deleteFolder(path string) string {
	target, err := validateDeleteTarget(path, readPrefsSnapshot())
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	if err := os.RemoveAll(target); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
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
	return target, nil
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
