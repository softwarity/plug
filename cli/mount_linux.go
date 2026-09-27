package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux mounts the share with the kernel's cifs module directly - mount(2)
// with the options the module parses itself - so cifs-utils' mount.cifs helper
// is not needed: plug holds cap_sys_admin (granted at install for the TUN)
// and that is what mount(2) asks. The mount is given the user's uid/gid so
// the command, which runs as the user, reads and writes it as its own.
//
// The child runs under a mount-namespace shim (tun.Run); the mount is made
// here, before the child exists, in the namespace it will clone from.

func mountSupported() error { return nil }

func mountBindAddr() string { return "127.0.0.1:0" }

func mountUsesForward() bool { return true }

// autoMountPath is where an automatic mount goes: under the session
// directory, at its cluster path.
func autoMountPath(dir, clusterPath string) string {
	return filepath.Join(dir, filepath.FromSlash(clusterPath))
}

func mountSMB(t mountTarget, path string) (string, error) {
	if err := ensureMountpoint(path); err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(t.local)
	if err != nil {
		return "", err
	}
	share, user, pass := t.share, t.user, t.pass
	uid, gid := os.Getuid(), os.Getgid()
	if u, g, ok := resolveDropTarget(os.Geteuid(), uid, gid, os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID")); ok {
		uid, gid = u, g
	}
	opts := fmt.Sprintf("ip=%s,port=%s,user=%s,pass=%s,vers=3.0,uid=%d,gid=%d,file_mode=0664,dir_mode=0775,noperm,nobrl",
		host, port, user, pass, uid, gid)
	if err := unix.Mount("//"+host+"/"+share, path, "cifs", 0, opts); err != nil {
		if errors.Is(err, unix.ENODEV) {
			return "", errors.New("the cifs filesystem is not available in this kernel (modprobe cifs, or install the kernel's extra modules)")
		}
		if errors.Is(err, unix.EPERM) {
			return "", errors.New("mount(2) refused: plug needs cap_sys_admin (re-run the install one-liner)")
		}
		return "", err
	}
	return path, nil
}

func unmountSMB(path string) error {
	if !mountedAt(path) {
		return nil
	}
	if err := unix.Unmount(path, 0); err != nil {
		// Busy: detach lazily, so the path is free now and the mount goes when
		// its last user does.
		if err2 := unix.Unmount(path, unix.MNT_DETACH); err2 != nil {
			return fmt.Errorf("%v; lazy: %v", err, err2)
		}
	}
	return nil
}

func mountedAt(path string) bool {
	var st, parent syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EIO) || errors.Is(err, unix.EHOSTDOWN)
	}
	if err := syscall.Lstat(path+"/..", &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}

func ensureMountpoint(path string) error {
	if mountedAt(path) {
		return fmt.Errorf("%s is already a mountpoint (a previous session's? plug doctor --fix)", path)
	}
	st, err := os.Stat(path)
	if err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	chownToUser(path)
	return nil
}

// pinMountName / unpinMountNames: nothing to pin here. The helper's name is
// dialled by this process through its own forward, never by the kernel
// through the machine's datapath (see mount_windows.go).
func pinMountName(config, string) {}

func unpinMountNames(config) {}
