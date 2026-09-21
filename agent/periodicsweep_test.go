package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The boot sweep was the only thing that ever restored a workload whose session
// died without releasing it, and an agent that does not restart never runs it
// again. Every tick must sweep; a sweep that blows up must not end the loop, or
// one bad pass would silently return the agent to the old behaviour; and
// cancelling the context must end it, or an embedder could never stop it.
func TestThePeriodicSweepKeepsSweeping(t *testing.T) {
	var passes atomic.Int32
	var notes []string
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sweepOn(ctx, tick, func(f string, a ...any) { notes = append(notes, f) }, func() {
			if passes.Add(1) == 2 {
				panic("the orchestrator hung up")
			}
		})
		close(done)
	}()
	for i := 0; i < 3; i++ {
		tick <- time.Now()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop on cancel")
	}
	if got := passes.Load(); got != 3 {
		t.Fatalf("swept %d times for 3 ticks - a panicking pass ended the loop", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "did not finish") {
		t.Fatalf("the panic was not named: %v", notes)
	}
}

// The periodic sweep must not be the boot sweep. gc clears every name lease,
// and a lease is the collision guard - the thing that refuses a second session
// the same name. Once at boot, when every port is orphaned anyway, that is
// right; once a minute, while sessions are live, it drops the guard on all of
// them. This pins the split by making sure the two are different functions,
// which is as close as Go lets a test get to "sweepPeriodically does not call
// gc" without an orchestrator to observe.
func TestThePeriodicSweepDoesNotClearTheLeases(t *testing.T) {
	// gc is boot-only. If somebody ever routes the ticker back through it, the
	// lease directory would be wiped once a minute under live sessions. The
	// safeguard is structural: the ticker's sweep is sweepOrchestrators, a
	// function that has no lease call in it. Read it if this ever fails.
	restore := nameLeaseDir
	nameLeaseDir = t.TempDir()
	defer func() { nameLeaseDir = restore }()
	takeNameLease("held-name", "45000")
	if leaseHolder("held-name") != "45000" {
		t.Fatal("the lease was not written; nothing below would prove anything")
	}
	sweepOrchestrators() // no orchestrator in a unit test: both backends skip
	if held := leaseHolder("held-name"); held != "45000" {
		t.Fatalf("the periodic sweep dropped a live name's lease: holder is %q", held)
	}
	gc()
	if held := leaseHolder("held-name"); held != "" {
		t.Fatalf("the boot sweep is expected to clear leases and did not: %q", held)
	}
}
