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
	// Start stamps: a parent is always older (smaller) than its child.
	starts := map[int]int64{100: 30, 42: 20, 7: 10}
	startOf := func(pid int) (int64, bool) { s, ok := starts[pid]; return s, ok }
	launchers := map[int]string{42: "hostB:2222"}
	clusterForPID := func(pid int) (string, bool) { k, ok := launchers[pid]; return k, ok }

	if key, ok := walkToCluster(100, ppidOf, startOf, clusterForPID); !ok || key != "hostB:2222" {
		t.Fatalf("app child → %q,%v want hostB:2222,true", key, ok)
	}
	if key, ok := walkToCluster(42, ppidOf, startOf, clusterForPID); !ok || key != "hostB:2222" {
		t.Fatalf("launcher itself → %q,%v want hostB:2222,true", key, ok)
	}
	// A process with no plug launcher in its ancestry → refuse (no wrong route).
	if _, ok := walkToCluster(7, ppidOf, startOf, clusterForPID); ok {
		t.Fatalf("no-plug ancestry must refuse")
	}
	// Unknown pid (broken chain) → refuse, no panic.
	if _, ok := walkToCluster(999, ppidOf, startOf, clusterForPID); ok {
		t.Fatalf("unknown pid must refuse")
	}
}

func TestWalkToClusterCycle(t *testing.T) {
	// A self-referential chain must terminate and refuse, not spin.
	ppidOf := func(pid int) (int, bool) { return pid, true }
	startOf := func(int) (int64, bool) { return 100, true }
	clusterForPID := func(int) (string, bool) { return "", false }
	if _, ok := walkToCluster(500, ppidOf, startOf, clusterForPID); ok {
		t.Fatalf("cycle must refuse")
	}
}

// TestWalkToClusterRecycledPID: an ancestor that started AFTER its child is a
// recycled PID, not a real parent — even though it (now) resolves to a cluster,
// the walk must refuse rather than misroute the flow to that stranger's cluster.
func TestWalkToClusterRecycledPID(t *testing.T) {
	parents := map[int]int{100: 42, 42: 7, 7: 1}
	ppidOf := func(pid int) (int, bool) { p, ok := parents[pid]; return p, ok }
	// 42 "started" (40) after its child 100 (30): the launcher died and PID 42 now
	// names a younger, unrelated process that happens to be registered.
	starts := map[int]int64{100: 30, 42: 40, 7: 10}
	startOf := func(pid int) (int64, bool) { s, ok := starts[pid]; return s, ok }
	clusterForPID := func(pid int) (string, bool) {
		if pid == 42 {
			return "hostB:2222", true
		}
		return "", false
	}
	if key, ok := walkToCluster(100, ppidOf, startOf, clusterForPID); ok {
		t.Fatalf("recycled ancestor must refuse, got %q — a misroute", key)
	}
}

func TestProcStartSelf(t *testing.T) {
	// The real per-OS procStart must return a positive, readable stamp for us.
	st, ok := procStart(os.Getpid())
	if !ok {
		t.Skip("procStart unavailable on this OS build")
	}
	if st <= 0 {
		t.Fatalf("procStart(self) = %d, want > 0", st)
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
