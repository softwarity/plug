//go:build unix

package agent

import (
	"fmt"
	"os"
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

// becomeIDs makes this whole process uid:gid, with that one group and no
// other, for good. Group first: once the uid is given up, nothing else can be.
func becomeIDs(uid, gid int) error {
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	return syscall.Setuid(uid)
}
