//go:build linux

package tun

import (
	"fmt"
	"os"
	"os/exec"
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

	// Are we actually in the namespace this function's whole design assumes?
	//
	// runChild re-execs plug with CLONE_NEWNS, so the shim lands in a fresh mount
	// namespace and the bind below is scoped to the command about to run. Nothing
	// enforced that. Invoked straight from a shell, the shim is in the MACHINE's
	// mount namespace, and both mounts below still succeed, because plug carries
	// cap_sys_admin as a file capability and file capabilities are granted on
	// exec whoever does the exec'ing.
	//
	// So any local user could run
	//     plug __plug-ns /tmp/theirs /bin/true
	// and replace the machine's /etc/resolv.conf with a file of their choosing,
	// for every account on the box, using plug's privilege rather than their own.
	//
	// The check compares this process's mount namespace with its PARENT's. Coming
	// from runChild they differ, because the clone made a new one. Coming from a
	// shell they are the same, and that is the case to refuse. It cannot be
	// forged: an attacker who arranges to be in a different namespace from their
	// own shell has unshared, and a mount inside a namespace they created affects
	// nobody but themselves. Comparing against PID 1 instead would have been
	// wrong inside a container, where the shim legitimately shares nothing with
	// init.
	if same, err := sameMountNamespaceAsParent(); err != nil {
		return fmt.Errorf("%s: cannot tell which mount namespace this is (%v), refusing to bind-mount", NsShimVerb, err)
	} else if same {
		return fmt.Errorf("%s is not a command: plug re-execs itself through it, inside a mount namespace\n"+
			"      it has just created for your command. Run from a shell it would bind-mount over the\n"+
			"      MACHINE's /etc/resolv.conf, for every account on it. Run `plug <your command>` instead", NsShimVerb)
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

// sameMountNamespaceAsParent reports whether this process shares its parent's
// mount namespace, which is how the shim tells "I was cloned into a new one" from
// "somebody ran me by hand". /proc/<pid>/ns/mnt reads back as an opaque identity;
// two processes in the same namespace read the same string.
func sameMountNamespaceAsParent() (bool, error) {
	mine, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false, err
	}
	theirs, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", os.Getppid()))
	if err != nil {
		// A parent that has already exited is not the shape this guard is aimed
		// at: runChild waits for its child. Refuse rather than guess.
		return false, err
	}
	return mine == theirs, nil
}
