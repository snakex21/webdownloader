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
	if path == "" {
		return "empty path"
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	parent := filepath.Dir(path)
	if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return "ok"
}

func (a *api) getLocale() string {
	return locale.Detect()
}

func (a *api) defaultOutputPath(rawURL string) string {
	executable, err := os.Executable()
	if err != nil {
		executable, _ = os.Getwd()
	}
	baseDir := filepath.Join(filepath.Dir(executable), "output")

	if rawURL == "" {
		return baseDir
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return filepath.Join(baseDir, strings.TrimPrefix(parsed.Hostname(), "www."))
	}
	return baseDir
}
