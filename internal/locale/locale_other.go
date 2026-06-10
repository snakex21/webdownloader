//go:build !windows

package locale

import "os"

// detectPlatform falls back to the LANG / LC_ALL / LC_MESSAGES env vars
// on non-Windows platforms. Returns "" if nothing is set.
func detectPlatform() string {
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}
