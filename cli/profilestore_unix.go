//go:build !windows

package main

import (
	"io/fs"
	"os"
	"syscall"
)

// dropTarget is the account plug acts for, and whether it holds a privilege
// over it (setuid on macOS, sudo); see resolveDropTarget.
func dropTarget() (uid, gid int, ok bool) {
	return resolveDropTarget(os.Geteuid(), os.Getuid(), os.Getgid(),
		os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"))
}

// fileOwner is the owner and the link count of a file, as stat gave them.
func fileOwner(fi fs.FileInfo) (uid int, links uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), uint64(st.Nlink), true
}

// chownFile hands a file to uid:gid on its descriptor: no path to resolve
// again, so no path to swap between the write and the chown.
func chownFile(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
