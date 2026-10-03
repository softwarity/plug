//go:build linux

package tun

import (
	"os"
	"os/exec"
	"syscall"
)

// runChild runs cmdArgs with our stdio, forwarding INT/TERM, and returns its exit
// code. When privResolv is set it launches the child in a fresh MOUNT namespace
// (CLONE_NEWNS) and, via the NsShimVerb re-exec, bind-mounts privResolv over
// /etc/resolv.conf THERE, so only the child resolves through plug and the global
// /etc/resolv.conf is never touched (concurrent `plug` runs stay isolated).
func runChild(cmdArgs []string, privResolv string) (int, error) {
	var child *exec.Cmd
	if privResolv != "" {
		self, err := os.Executable()
		if err != nil {
			self = "/proc/self/exe"
		}
		child = exec.Command(self, append([]string{NsShimVerb, privResolv}, cmdArgs...)...)
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	} else {
		// No private resolv.conf to bind, so no re-exec through NsShimVerb - and
		// that shim is where the AMBIENT capability set was being cleared. Which
		// meant this branch handed the user's command whatever the launcher had
		// raised so it would survive exec'ing a downloaded core: CAP_SYS_ADMIN,
		// CAP_NET_ADMIN, CAP_NET_BIND_SERVICE. Not hypothetical - anything that
		// makes the private resolv.conf fail to be created (an unwritable or
		// invalid TMPDIR is enough) lands here, and the session then runs `npm
		// run dev` with the capabilities plug holds.
		//
		// Cleared in THIS process, before starting the child, on purpose. Ambient
		// caps only exist to cross an exec; dropping them takes nothing away from
		// the permitted or effective set this process still holds, and the TUN
		// device is long since open by the time a command is run. The shim branch
		// above must NOT do this: it re-execs plug itself, which needs them to
		// cross that exec, and clears them on the far side once its mounts are
		// done.
		clearAmbientCaps()
		child = exec.Command(cmdArgs[0], cmdArgs[1:]...)
	}
	return waitChild(child)
}
