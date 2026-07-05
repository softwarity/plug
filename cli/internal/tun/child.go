package tun

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// runChild runs cmdArgs inheriting our stdio, forwards INT/TERM to it, and
// returns its exit code (127 if it can't start).
func runChild(cmdArgs []string) (int, error) {
	child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := child.Start(); err != nil {
		return 127, err
	}
	go func() {
		for s := range sigs {
			_ = child.Process.Signal(s)
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
