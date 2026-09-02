//go:build linux

package tun

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// NsShimMain runs inside the child's fresh mount namespace (entered via the
// CLONE_NEWNS re-exec in runChild). It makes mount propagation private so the
// bind never escapes to the host, bind-mounts the private resolv.conf over
// /etc/resolv.conf, then execs the real command. args = [privResolv, cmd, args…].
func NsShimMain(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s: need <resolv> <cmd>...", NsShimVerb)
	}
	priv, cmd := args[0], args[1:]

	// Make our own mount namespace before touching anything, so this bind can
	// never land on the machine's /etc/resolv.conf.
	//
	// runChild re-execs plug with CLONE_NEWNS and the shim lands in a fresh
	// namespace, which is what makes the bind below safe. Nothing enforced that.
	// Run straight from a shell the shim is in the MACHINE's namespace, and both
	// mounts still succeed, because plug carries CAP_SYS_ADMIN as a file
	// capability and file capabilities are granted on exec whoever does the
	// exec'ing. Any local account could therefore point every other account's
	// resolver at a file of its own, using plug's privilege rather than its own.
	//
	// Detecting the situation was the wrong instinct: comparing our mount
	// namespace with the parent's means reading /proc/<ppid>/ns/mnt, and the
	// parent has raised capabilities, which makes it non-dumpable and that link
	// unreadable even to the same user. The e2e said so on all three Linux legs
	// within the hour. Unsharing needs no permission we do not already have and
	// needs to detect nothing: from runChild it copies an already fresh namespace,
	// which changes nothing; from a shell it contains the mount to a namespace
	// this process just made, which is the whole fix.
	//
	// The thread lock matters. A mount namespace is a property of the TASK, so the
	// unshare applies to the thread that made the call, and the Go runtime is free
	// to move a goroutine between threads: the mounts below would then run
	// somewhere the unshare never happened. Locked, they and the exec that follows
	// stay on the thread that owns the new namespace.
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("%s: cannot create a private mount namespace (%w), refusing to bind-mount", NsShimVerb, err)
	}

	// MS_PRIVATE on / first: without it the bind-mount could propagate back into
	// the host mount namespace (systemd makes / shared by default) — the exact
	// global leak we're avoiding.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := syscall.Mount(priv, "/etc/resolv.conf", "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind private resolv.conf: %w", err)
	}

	// The launcher may have raised plug's capabilities into the AMBIENT set so
	// they survive exec'ing a downloaded core (raiseAmbientCaps). The mounts above
	// are done — drop the ambient set NOW so the USER's command below inherits no
	// privilege from plug.
	clearAmbientCaps()

	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		bin = cmd[0]
	}
	return syscall.Exec(bin, cmd, os.Environ()) // replaces this process on success
}

// clearAmbientCaps drops the ambient set, so nothing exec'd afterwards inherits
// the privilege plug carries. Best effort by design: a failure here means the
// caps were never raised (the unprivileged path), which is the safe direction.
func clearAmbientCaps() {
	_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0)
}
