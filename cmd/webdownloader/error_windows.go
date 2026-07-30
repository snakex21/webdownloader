//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	messageBoxOK            = 0x00000000
	messageBoxIconError     = 0x00000010
	messageBoxSetForeground = 0x00010000
)

var procMessageBoxW = user32.NewProc("MessageBoxW")

func showFatalError(title, message string) {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	messagePtr, _ := windows.UTF16PtrFromString(message)
	procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(messagePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		messageBoxOK|messageBoxIconError|messageBoxSetForeground,
	)
}
