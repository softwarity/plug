//go:build windows

package main

import "os"

// processAlive reports whether pid is a live process. Windows has no signal 0:
// os.FindProcess opens the process and fails when there is nothing to open,
// which is the check. It is the weaker of the two — a terminated process whose
// handles are still held can be opened — so on Windows the record is a hint the
// command line has to confirm. That is what it is presented as either way.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
