//go:build !windows

package tunnel

import (
	"os"
	"syscall"
)

// The platform half of the known_hosts guard (see openKnownHosts and
// writeKnownHosts in transport.go): how to refuse a link at open time, how to
// read a file's owner, and how to hand a file to the person.

// noFollow makes the open itself refuse a symlink, closing the window between
// the Lstat and the open that a check by path alone leaves.
const noFollow = syscall.O_NOFOLLOW

// fileOwner is the uid and gid the file system recorded for fi.
func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// chownTo hands path to uid:gid. Lchown, not Chown, for the same reason as in
// the launcher: on a link Chown follows it and would hand the TARGET over.
func chownTo(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }
