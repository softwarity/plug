//go:build darwin

package tun

import (
	"os"
	"os/exec"
	"testing"
)

func TestRegistryLiveClients(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	if n := LiveClients(key); n != 0 {
		t.Fatalf("empty registry should have 0 clients, got %d", n)
	}

	un := RegisterClient(key, os.Getpid())
	if n := LiveClients(key); n != 1 {
		t.Fatalf("after register, want 1 live client, got %d", n)
	}

	// A dead PID marker must be reaped, not counted (the kill -9 case).
	RegisterClient(key, spawnAndKill(t))
	if n := LiveClients(key); n != 1 {
		t.Fatalf("dead client must be reaped, want 1, got %d", n)
	}

	un()
	if n := LiveClients(key); n != 0 {
		t.Fatalf("after unregister, want 0, got %d", n)
	}
}

func TestDaemonAlive(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-b:2222"
	if DaemonAlive(key) {
		t.Fatal("no daemon yet → DaemonAlive must be false")
	}
	leader, release, _ := AcquireCluster(key)
	if !leader {
		t.Fatal("first AcquireCluster should lead")
	}
	defer release()
	if !DaemonAlive(key) {
		t.Fatal("the lock is held → DaemonAlive must be true")
	}
	if pid := DaemonPID(key); pid != os.Getpid() {
		t.Fatalf("DaemonPID = %d, want this process %d", pid, os.Getpid())
	}
}

// spawnAndKill starts a process, kills and reaps it, and returns its now-dead PID.
func spawnAndKill(t *testing.T) int {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Process.Kill()
	_, _ = c.Process.Wait() // reap the zombie so kill(pid,0) reports ESRCH
	return pid
}
