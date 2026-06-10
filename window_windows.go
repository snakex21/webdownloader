//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultWindowWidth  = 1100
	defaultWindowHeight = 820
	minWindowWidth      = 900
	minWindowHeight     = 720
	maxWindowWidth      = 3840
	maxWindowHeight     = 2160
)

type windowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

var user32 = windows.NewLazySystemDLL("user32.dll")
var procGetWindowRect = user32.NewProc("GetWindowRect")

func loadWindowSize() (int, int) {
	prefs := readPrefsMap()
	w := intFromPref(prefs["windowWidth"], defaultWindowWidth)
	h := intFromPref(prefs["windowHeight"], defaultWindowHeight)
	return clampWindowWidth(w), clampWindowHeight(h)
}

func saveWindowSize(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	var r windowRect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return
	}
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w < minWindowWidth || h < minWindowHeight {
		return
	}
	prefs := readPrefsMap()
	prefs["windowWidth"] = clampWindowWidth(w)
	prefs["windowHeight"] = clampWindowHeight(h)
	if data, err := jsonMarshalPrefs(prefs); err == nil {
		_ = writePrefsBytes(data)
	}
}

func intFromPref(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}

func clampWindowWidth(v int) int {
	if v < minWindowWidth {
		return minWindowWidth
	}
	if v > maxWindowWidth {
		return maxWindowWidth
	}
	return v
}

func clampWindowHeight(v int) int {
	if v < minWindowHeight {
		return minWindowHeight
	}
	if v > maxWindowHeight {
		return maxWindowHeight
	}
	return v
}
