//go:build !windows

package main

import "os"

// moveToTrash deletes path (no portable Recycle Bin outside Windows).
func moveToTrash(path string) error { return os.RemoveAll(path) }
