// Package locale detects the system UI language.
//
// The Windows implementation uses GetUserDefaultLocaleName from kernel32
// (a single direct WinAPI call — no subprocess, no PATH lookup, no
// PowerShell). This works reliably inside the webview process where the
// env may be very different from an interactive user shell.
//
// On non-Windows platforms we fall back to the LANG/LC_ALL environment
// variables. All implementations return the two-letter ISO 639-1 code
// (e.g. "en", "pl"); unknown values yield "en".
package locale

import "strings"

// Detect returns the system UI language as a two-letter code.
func Detect() string {
	if code := detectPlatform(); code != "" {
		return shortCode(code)
	}
	return "en"
}

// shortCode normalises a locale tag (e.g. "pl-PL", "en_US.UTF-8",
// "kok-Latn-IN") to its two-letter ISO 639-1 form.
func shortCode(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	tag = strings.ReplaceAll(tag, "_", "-")
	if i := strings.Index(tag, "-"); i > 0 {
		tag = tag[:i]
	}
	if i := strings.Index(tag, "."); i > 0 {
		tag = tag[:i]
	}
	tag = strings.ToLower(tag)
	if len(tag) > 2 {
		tag = tag[:2]
	}
	return tag
}
