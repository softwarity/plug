//go:build darwin

package tun

import (
	"os"
	"testing"
)

// TestAcquireClusterLeaderThenGraft checks the per-cluster lock: the first caller
// leads, a second caller for the SAME cluster grafts (does not lead), a different
// cluster leads on its own, and once the leader releases the slot frees again.
func TestAcquireClusterLeaderThenGraft(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	leader1, rel1, err := AcquireCluster("host-a:2222")
	if err != nil || !leader1 {
		t.Fatalf("first acquire should lead: leader=%v err=%v", leader1, err)
	}

	leader2, rel2, _ := AcquireCluster("host-a:2222")
	if leader2 {
		t.Fatal("second acquire for the same cluster must graft, not lead")
	}
	rel2()

	leader3, rel3, _ := AcquireCluster("host-b:2222")
	if !leader3 {
		t.Fatal("a different cluster must lead on its own")
	}
	rel3()

	// Once the leader releases, the slot frees for a new leader.
	rel1()
	leader4, rel4, _ := AcquireCluster("host-a:2222")
	if !leader4 {
		t.Fatal("after the leader releases, a new leader must be possible")
	}
	rel4()
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
