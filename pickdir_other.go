//go:build !windows

package main

import "errors"

// pickFolderWindows is a no-op on non-Windows platforms; the caller
// (api.pickFolder) will fall back to the default output path.
func pickFolderWindows(hwnd uintptr) (string, error) {
	return "", errors.New("native picker not supported on this platform")
}
