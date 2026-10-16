//go:build !windows

package main

import "path/filepath"

// realPath is a path as the file it opens. The key guards that compare it run
// on Windows only (see realpath_windows.go); elsewhere links are all a path
// can hide behind.
func realPath(abs string) string {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}
