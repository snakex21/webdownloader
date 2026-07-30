//go:build !windows

package main

import "log"

func showFatalError(title, message string) {
	log.Printf("%s: %s", title, message)
}
