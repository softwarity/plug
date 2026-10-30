//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// signalDuringRun runs a --dockerrun against the fake docker and sends sig to
// this process while the user's container runs, the way a terminal's Ctrl-C
// reaches plug (the container gets its own copy from the kernel, which a
// targeted signal here does not model) or a `kill` names it. It returns plug's
// status and the calls docker saw.
func signalDuringRun(t *testing.T, sig syscall.Signal) (int, []fakeCall) {
	t.Helper()
	log := installFakeDocker(t)
	t.Setenv("PLUG_DOCKER_IMAGE", "softwarity/plug:test")
	t.Setenv("PLUG_DOCKER_NETWORK", "")
	hold := filepath.Join(t.TempDir(), "release")
	t.Setenv(fakeDockerHoldEnv, hold)
	saved := dockerMounts
	dockerMounts = nil
	t.Cleanup(func() { dockerMounts = saved })
	cfg := config{host: "h.example", port: "2200"}
	cfg.envPolicy.off = true
	cfg.mountPolicy.off = true

	// Up to the fake container's own deadline: under -race on a CI runner,
	// the docker calls before it (each one this test binary started again)
	// take seconds, and a signal never sent fails the test as a lost one.
	go func() {
		for i := 0; i < 2000; i++ {
			if _, err := os.Stat(hold + ".started"); err == nil {
				_ = syscall.Kill(os.Getpid(), sig)
				// What the kernel's copy does to a container that answers
				// SIGINT: it ends. A SIGTERM must reach it through plug alone.
				if sig == syscall.SIGINT {
					_ = os.WriteFile(hold, nil, 0o600)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	code := runDockerRun(cfg, []string{"docker", "run", "--rm", "busybox", "true"}, nil, true)
	return code, readFakeCalls(t, log)
}

// tornDown says whether the teardown ran: the sidecar's stdin closed and the
// sidecar removed after the container ended.
func tornDown(calls []fakeCall) (stdinClosed, removed bool) {
	for _, c := range calls {
		if len(c.Args) > 1 && c.Args[0] == "run" && c.Args[1] == "-i" && c.HoldEOF {
			stdinClosed = true
		}
		if len(c.Args) > 2 && c.Args[0] == "rm" && c.Args[1] == "-f" && strings.HasPrefix(c.Args[2], "plug-net-") {
			removed = true
		}
	}
	return
}

// A Ctrl-C is the container's to answer: plug survives it, waits for the
// container, and tears down what it made. It used to end plug on the spot,
// skipping every step of the teardown: the sidecar held the session on, and
// the environment file and the cifs volumes stayed where they were.
func TestACtrlCLetsTheDockerRunTearDown(t *testing.T) {
	code, calls := signalDuringRun(t, syscall.SIGINT)
	if code != 0 {
		t.Errorf("the container ended normally on its Ctrl-C and plug reported %d", code)
	}
	if closed, removed := tornDown(calls); !closed || !removed {
		t.Errorf("after a Ctrl-C the sidecar's stdin closed: %v, the sidecar removed: %v; calls: %v", closed, removed, calls)
	}
}

// A SIGTERM at plug alone is not delivered to the group: plug passes it to the
// docker CLI, which passes it to the container, then tears down.
func TestASIGTERMAtPlugReachesTheContainer(t *testing.T) {
	code, calls := signalDuringRun(t, syscall.SIGTERM)
	var got string
	for _, c := range calls {
		if len(c.Args) > 1 && c.Args[0] == "run" && c.Args[1] != "-i" {
			got = c.Signalled
		}
	}
	if got != syscall.SIGTERM.String() {
		t.Errorf("the container was ended by %q, want the SIGTERM sent to plug", got)
	}
	if code != 143 {
		t.Errorf("plug reported %d, want the container's 143", code)
	}
	if closed, removed := tornDown(calls); !closed || !removed {
		t.Errorf("after a SIGTERM the sidecar's stdin closed: %v, the sidecar removed: %v", closed, removed)
	}
}
