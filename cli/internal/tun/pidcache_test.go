package tun

// What the attribution may remember between two flows, and what it must not.
//
// Multicluster attribution costs a fork per hop: one `ps -o lstart=` and one
// `ps -o ppid=` per ancestor, each measured at ~16ms on a real Mac, on top of the
// ~80ms lsof that finds the PID in the first place. A shell -> plug -> app chain
// therefore paid five forks per CONNECTION, and a database pool opening ten of
// them paid it ten times over for an answer that had not changed.
//
// The ancestry of a live process does not change. What CAN change under it is the
// PID itself, recycled onto an unrelated process, and misrouting a flow to
// another developer's cluster is precisely what this code refuses to do. So the
// stamp is re-read on every flow and the rest is remembered.

import (
	"sync"
	"testing"
)

// countingChain is a fake ancestry that records how often each primitive is asked.
type countingChain struct {
	mu       sync.Mutex
	parent   map[int]int
	start    map[int]int64
	launcher map[int]string
	nStart   int
	nPpid    int
	nCluster int
}

func (c *countingChain) startOf(pid int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nStart++
	s, ok := c.start[pid]
	return s, ok
}

func (c *countingChain) ppidOf(pid int) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nPpid++
	p, ok := c.parent[pid]
	return p, ok
}

func (c *countingChain) clusterForPID(pid int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nCluster++
	k, ok := c.launcher[pid]
	return k, ok
}

// app 300 -> plug 200 (the launcher) -> shell 100
func newChain() *countingChain {
	return &countingChain{
		parent:   map[int]int{300: 200, 200: 100, 100: 1},
		start:    map[int]int64{300: 30, 200: 20, 100: 10, 1: 1},
		launcher: map[int]string{200: "clusterA:2222"},
	}
}

func TestTheAncestryIsWalkedOncePerProcessNotPerFlow(t *testing.T) {
	c := newChain()
	r := pidRouter{
		clusterForPID: c.clusterForPID,
		pidForConn:    func(uint16) (int, bool) { return 300, true },
		ppidOf:        c.ppidOf,
		startOf:       c.startOf,
		seen:          newPidCache(),
	}

	for i := 0; i < 10; i++ {
		key, ok := r.route(uint16(40000 + i))
		if !ok || key != "clusterA:2222" {
			t.Fatalf("flow %d routed to %q,%v", i, key, ok)
		}
	}

	// The walk itself: once. Ten flows from one process must not re-climb it.
	if c.nPpid > 2 {
		t.Errorf("ppidOf called %d times for 10 flows from one process, want the walk done once", c.nPpid)
	}
	if c.nCluster > 3 {
		t.Errorf("clusterForPID called %d times for 10 flows, want the walk done once", c.nCluster)
	}
	// The recycle guard, on the other hand, must run EVERY time: it is the only
	// thing standing between a cached answer and a PID that now names a stranger.
	if c.nStart < 10 {
		t.Errorf("startOf called %d times for 10 flows: the recycled-PID check was cached away", c.nStart)
	}
}

// A PID reused by an unrelated process must not inherit the answer. This is the
// case the whole walk exists for, and a cache is exactly how it gets lost.
func TestARecycledPidDoesNotInheritTheCachedCluster(t *testing.T) {
	c := newChain()
	r := pidRouter{
		clusterForPID: c.clusterForPID,
		pidForConn:    func(uint16) (int, bool) { return 300, true },
		ppidOf:        c.ppidOf,
		startOf:       c.startOf,
		seen:          newPidCache(),
	}
	if key, ok := r.route(40000); !ok || key != "clusterA:2222" {
		t.Fatalf("first flow routed to %q,%v", key, ok)
	}

	// PID 300 is now somebody else: born later, child of init, no launcher above.
	c.mu.Lock()
	c.start[300] = 999
	c.parent[300] = 1
	c.mu.Unlock()

	if key, ok := r.route(40001); ok {
		t.Errorf("a recycled PID kept the cached route %q: that is a flow sent to the wrong cluster", key)
	}
}

// A refusal must not be cached as a refusal forever: the launcher registry is
// written by a client that may not have registered yet when the first flow lands.
func TestARefusalIsNotRememberedAsOne(t *testing.T) {
	c := newChain()
	delete(c.launcher, 200) // nothing registered yet
	r := pidRouter{
		clusterForPID: c.clusterForPID,
		pidForConn:    func(uint16) (int, bool) { return 300, true },
		ppidOf:        c.ppidOf,
		startOf:       c.startOf,
		seen:          newPidCache(),
	}
	if _, ok := r.route(40000); ok {
		t.Fatal("routed with no launcher registered")
	}
	c.mu.Lock()
	c.launcher[200] = "clusterA:2222"
	c.mu.Unlock()
	if key, ok := r.route(40001); !ok || key != "clusterA:2222" {
		t.Errorf("after the launcher registered, the flow was still refused (%q,%v)", key, ok)
	}
}

// Two processes, two clusters, one router: the cache must not blur them.
func TestTwoProcessesKeepTheirOwnCluster(t *testing.T) {
	c := newChain()
	c.parent[500] = 400
	c.start[500] = 50
	c.start[400] = 40
	c.parent[400] = 1
	c.launcher[400] = "clusterB:2222"

	var which int
	r := pidRouter{
		clusterForPID: c.clusterForPID,
		pidForConn:    func(uint16) (int, bool) { return which, true },
		ppidOf:        c.ppidOf,
		startOf:       c.startOf,
		seen:          newPidCache(),
	}
	for i := 0; i < 5; i++ {
		which = 300
		if key, ok := r.route(40000); !ok || key != "clusterA:2222" {
			t.Fatalf("A routed to %q,%v", key, ok)
		}
		which = 500
		if key, ok := r.route(40001); !ok || key != "clusterB:2222" {
			t.Fatalf("B routed to %q,%v", key, ok)
		}
	}
}
