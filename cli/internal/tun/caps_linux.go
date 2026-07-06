//go:build linux

package tun

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	capNetBindService = 10 // bind :53
	capSysAdmin       = 21 // mount namespace
)

// withPrivCaps passes plug's network/mount capabilities down to a helper it
// shells out to (ip/route/sysctl). File capabilities are per-binary and do NOT
// survive an exec, so a setcap'd, non-root plug would otherwise run `ip` with no
// privileges. Ambient capabilities carry them across the exec. No-op as root
// (the child is already privileged) — and plug only reaches here as root or with
// these caps in its permitted set (checkPriv), so raising them always succeeds.
func withPrivCaps(cmd *exec.Cmd) {
	if os.Geteuid() == 0 {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		AmbientCaps: []uintptr{capNetAdmin, capSysAdmin, capNetBindService},
	}
}
