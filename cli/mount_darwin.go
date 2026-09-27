package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// macOS mounts an SMB share with mount_smbfs, which ships with the system and
// needs no privilege: the mount belongs to the user who made it. So it is run
// AS THE USER, not as the setuid root plug is - the same drop the command
// gets (applyPrivDrop) - which is exactly what makes the mount the user's to
// read and the command's to write, and leaves nothing root-owned behind.
//
// What can still say "Operation not permitted" on a mount that succeeded is
// TCC: since Catalina a process only touches a NETWORK VOLUME if the app it
// runs under has that permission (System Settings > Privacy & Security >
// Files and Folders > Network Volumes). Terminal asks once; an app that never
// asked gets EPERM. Seen from a shell under an editor; doctor names it.

func mountSupported() error { return nil }

func mountBindAddr() string { return "127.0.0.1:0" }

func mountSMB(local, share, user, pass, path string) error {
	if err := ensureMountpoint(path); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(local)
	if err != nil {
		return err
	}
	if err := ensureLoopbackSMBConfig(host); err != nil {
		return err
	}
	// //user:pass@host:port/share - the password is hex, so it needs no
	// escaping, and it lives on this one short-lived command line only.
	target := fmt.Sprintf("//%s:%s@%s:%s/%s", url.PathEscape(user), pass, host, port, share)
	cmd := exec.Command("/sbin/mount_smbfs", "-N", target, path)
	applyPrivDrop(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	// Mounted; can this process, and so the command (same responsible app),
	// look inside? TCC decides that per APP, not per user, and answers EPERM
	// on a mount that is otherwise perfect. Say what it is and where the
	// switch lives, once, rather than let the command fail on every file.
	if _, err := os.ReadDir(path); err != nil && errors.Is(err, syscall.EPERM) {
		info("macOS refuses this process access to network volumes (%s: %v).", path, err)
		info("      Allow the app you run plug from (Terminal, iTerm, your editor) under System Settings >")
		info("      Privacy & Security > Files and Folders > Network Volumes, or run plug from Terminal.")
	}
	return nil
}

// unmountSMB unmounts, trying umount first and diskutil after it: umount runs
// under the caller's TCC identity and may be refused where diskarbitrationd,
// which diskutil asks, is not.
func unmountSMB(path string) error {
	if !mountedAt(path) {
		return nil
	}
	if out, err := exec.Command("/sbin/umount", path).CombinedOutput(); err == nil {
		return nil
	} else if out2, err2 := exec.Command("/usr/sbin/diskutil", "unmount", path).CombinedOutput(); err2 != nil {
		return fmt.Errorf("%s; %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
	}
	return nil
}

// mountedAt reports whether path is a mountpoint: its device differs from its
// parent's. A dead network mount may refuse to be stat'd at all, which is
// still "something is mounted there".
func mountedAt(path string) bool {
	var st, parent syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EPERM)
	}
	if err := syscall.Lstat(path+"/..", &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}

// ensureMountpoint creates the directory as the user and refuses a path that
// is already something else (a file, a busy mount).
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

// nsmbSection is what plug adds to the user's nsmb.conf, once, scoped to the
// loopback server every plug mount goes through. SMB multichannel is the one
// setting that has to be off there: after a reconnect (a tunnel blip, an
// agent restart) macOS re-negotiates and re-attaches the share fine, then
// its multichannel bookkeeping looks for the NIC behind 127.0.0.1, finds none
// ("smb2_mc_update_main_channel: could not find one of the nics", error 22)
// and drops the session it had just recovered - the mount goes with it. On
// one interface multichannel has nothing to offer anyway. Nothing else is
// touched, and a section for another server is never read for this one.
const nsmbSection = "\n# Added by plug (--mount): multichannel has no second NIC on loopback and\n" +
	"# kills a reconnected mount. Scoped to plug's own server; remove freely.\n" +
	"[%s]\nmc_on=no\n"

// nsmbConfPath is the REAL user's file: under sudo, HOME may be root's, and
// mount_smbfs runs as the user (applyPrivDrop), reading the user's own.
func nsmbConfPath() string {
	home := realUserHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Preferences", "nsmb.conf")
}

// ensureLoopbackSMBConfig appends the section when the file does not carry it
// yet. Written as the user's (chownToUser), like everything under the home.
func ensureLoopbackSMBConfig(server string) error {
	path := nsmbConfPath()
	if path == "" {
		return nil
	}
	existing, _ := os.ReadFile(path)
	if hasNsmbSection(existing, server) {
		return nil
	}
	guardUserPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("cannot write %s (SMB multichannel must be off on loopback): %w", path, err)
	}
	_, werr := fmt.Fprintf(f, nsmbSection, server)
	f.Close()
	chownToUser(path)
	return werr
}

// hasNsmbSection reports whether conf already has a [server] section.
func hasNsmbSection(conf []byte, server string) bool {
	for _, line := range strings.Split(string(conf), "\n") {
		if strings.TrimSpace(line) == "["+server+"]" {
			return true
		}
	}
	return false
}
