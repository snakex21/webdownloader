//go:build !windows

package main

const (
	defaultWindowWidth  = 1100
	defaultWindowHeight = 820
)

func loadWindowSize() (int, int) {
	return defaultWindowWidth, defaultWindowHeight
}

func saveWindowSize(_ uintptr) {}
