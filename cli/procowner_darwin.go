package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processUID is the real uid of pid, from the kernel's process table. The
// launcher runs setuid here, so a signal it sends reaches any process on the
// machine; this is what makes "stop the session holding that name" a question
// about the person's own processes only.
func processUID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("PID %d: %v", pid, err)
	}
	return int(kp.Eproc.Ucred.Uid), nil
}
