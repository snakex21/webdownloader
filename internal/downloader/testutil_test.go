package downloader

import "os"

// readFileOS is a tiny shim used by tests so we don't have to repeat the
// os import in every test file.
func readFileOS(p string) ([]byte, error) {
	return os.ReadFile(p)
}
