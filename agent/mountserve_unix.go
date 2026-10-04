//go:build unix

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ownerIDs is the uid and gid owning path, asked of the kernel.
func ownerIDs(path string) (uid, gid int, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("no owner in what stat answered for %s", path)
	}
	return int(st.Uid), int(st.Gid), nil
}

// runAs makes cmd start under uid:gid, with that one group and no other.
func runAs(cmd *exec.Cmd, uid, gid int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
}
