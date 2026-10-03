package tun

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// waitChild starts child on our stdio, relays a targeted SIGTERM to it, waits
// for it and returns its exit code: 127 when it could not start, the child's
// own code when it ran, 1 with the error for anything else. The per-OS
// runChild builds the command (a mount-namespace re-exec on Linux, the bare
// command elsewhere); what happens once it exists is the same everywhere.
func waitChild(child *exec.Cmd) (int, error) {
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := child.Start(); err != nil {
		return 127, err
	}
	go func() {
		for s := range sigs {
			// Ctrl-C (SIGINT) is group-delivered: the child already has it;
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
