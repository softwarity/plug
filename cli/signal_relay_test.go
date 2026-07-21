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

// TestChildSignalRelay pins the Ctrl-C contract: SIGINT is NOT re-sent to the
// child — the kernel already delivers a terminal Ctrl-C to the whole
// foreground group, and the resend made dev servers see a double SIGINT and
// force-quit without restoring the tty (arrow keys then echoed ^[[A in the
// shell). A TARGETED SIGTERM at plug alone is not group-delivered, so that
// one IS relayed.
func TestChildSignalRelay(t *testing.T) {
	log := filepath.Join(t.TempDir(), "sig.log")
	script := "trap 'echo INT >> " + log + "' INT\n" +
		"trap 'echo TERM >> " + log + "; exit 0' TERM\n" +
		"echo ready >> " + log + "\n" +
		"n=0; while [ $n -lt 100 ]; do sleep 0.1; n=$((n+1)); done"
	done := make(chan int, 1)
	go func() { done <- runChildEnv([]string{"/bin/sh", "-c", script}, nil) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(log); strings.Contains(string(b), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Targeted SIGINT at THIS process only: runChildEnv catches it (plug must
	// survive to run its teardown) and must NOT relay it to the child.
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)

	// Targeted SIGTERM: relayed — the child's handler logs it and exits 0.
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("child exit %d, want 0 (clean TERM handler)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after the relayed SIGTERM")
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "INT") {
		t.Fatalf("the child saw a relayed SIGINT — a terminal Ctrl-C would be doubled:\n%s", b)
	}
	if !strings.Contains(string(b), "TERM") {
		t.Fatalf("the child never saw the relayed SIGTERM:\n%s", b)
	}
}
