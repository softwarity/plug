//go:build windows

package main

import "os"

// ttyDevice is the console input handle — the Windows equivalent of /dev/tty.
const ttyDevice = "CONIN$"

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

// holderIsMine has nothing to check here, and that is a statement about the
// process model rather than an omission: the launcher is never elevated on
// Windows (the SYSTEM service holds the datapath, the launcher runs as the
// person), so the signal stopHolder sends carries the person's own rights and
// the kernel refuses another account's process by itself. The account check
// by SID that would make this symmetrical with unix belongs with the per-flow
// account work in the registry, not here.
func holderIsMine(int) error { return nil }
