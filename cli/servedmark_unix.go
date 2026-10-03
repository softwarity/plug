//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// ttyDevice is the terminal to ask a question on, whatever stdin was redirected to.
const ttyDevice = "/dev/tty"

// processAlive reports whether pid is a live process. Signal 0 performs the
// permission and existence checks without delivering anything — the standard
// probe. EPERM would mean "alive but not ours", which cannot happen for a
// record this user wrote.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid) // always succeeds on unix
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// holderIsMine refuses to signal a process that does not belong to the account
// plug acts for. The record under ~/.plug/served is the user's own, but the PID
// in it can have been recycled since, and on macOS the signal goes out with
// euid 0: without this, a stale record plus a "yes" would stop a process of any
// account on the machine. processUID is per OS (procowner_<os>.go).
func holderIsMine(pid int) error {
	owner, err := processUID(pid)
	if err != nil {
		return err
	}
	if me := realUID(); owner != me {
		return fmt.Errorf("PID %d belongs to uid %d, not to you (uid %d): the record under ~/.plug/served is stale "+
			"(its PID was reused), so it is not stopped. Remove the record and run again", pid, owner, me)
	}
	return nil
}
