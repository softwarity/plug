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
	_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0)

	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		bin = cmd[0]
	}
	return syscall.Exec(bin, cmd, os.Environ()) // replaces this process on success
}
