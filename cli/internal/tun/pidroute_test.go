//go:build !windows

package tun

import (
	"os"
	"testing"
)

func TestWalkToCluster(t *testing.T) {
	// Synthetic ancestry: 100 (app's child) → 42 (plug -p B) → 7 (shell) → 1.
	parents := map[int]int{100: 42, 42: 7, 7: 1}
	ppidOf := func(pid int) (int, bool) { p, ok := parents[pid]; return p, ok }
	launchers := map[int]string{42: "hostB:2222"}
	clusterForPID := func(pid int) (string, bool) { k, ok := launchers[pid]; return k, ok }

	if key, ok := walkToCluster(100, ppidOf, clusterForPID); !ok || key != "hostB:2222" {
		t.Fatalf("app child → %q,%v want hostB:2222,true", key, ok)
	}
	if key, ok := walkToCluster(42, ppidOf, clusterForPID); !ok || key != "hostB:2222" {
		t.Fatalf("launcher itself → %q,%v want hostB:2222,true", key, ok)
	}
	// A process with no plug launcher in its ancestry → refuse (no wrong route).
	if _, ok := walkToCluster(7, ppidOf, clusterForPID); ok {
		t.Fatalf("no-plug ancestry must refuse")
	}
	// Unknown pid (broken chain) → refuse, no panic.
	if _, ok := walkToCluster(999, ppidOf, clusterForPID); ok {
		t.Fatalf("unknown pid must refuse")
	}
}

func TestWalkToClusterCycle(t *testing.T) {
	// A self-referential chain must terminate and refuse, not spin.
	ppidOf := func(pid int) (int, bool) { return pid, true }
	clusterForPID := func(int) (string, bool) { return "", false }
	if _, ok := walkToCluster(500, ppidOf, clusterForPID); ok {
		t.Fatalf("cycle must refuse")
	}
}

func TestPpidOfSelf(t *testing.T) {
	// The real per-OS ppidOf must agree with the runtime for our own process.
	ppid, ok := ppidOf(os.Getpid())
	if !ok {
		t.Skip("ppidOf unavailable on this OS build")
	}
	if ppid != os.Getppid() {
		t.Fatalf("ppidOf(self) = %d, want %d", ppid, os.Getppid())
	}
}
