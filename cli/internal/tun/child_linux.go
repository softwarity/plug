//go:build linux

package tun

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// runChild runs cmdArgs with our stdio, forwarding INT/TERM, and returns its exit
// code. When privResolv is set it launches the child in a fresh MOUNT namespace
// (CLONE_NEWNS) and — via the NsShimVerb re-exec — bind-mounts privResolv over
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
		child = exec.Command(cmdArgs[0], cmdArgs[1:]...)
	}
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := child.Start(); err != nil {
		return 127, err
	}
	go func() {
		for s := range sigs {
			// Ctrl-C (SIGINT) is group-delivered — the child already has it;
			// re-sending doubled it and dev servers force-quit on the second
			// without restoring the tty. Only a targeted SIGTERM is relayed.
			if s == syscall.SIGTERM {
				_ = child.Process.Signal(s)
			}
		}
	}()

	err := child.Wait()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return 1, err
}
