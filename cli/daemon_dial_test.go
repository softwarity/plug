//go:build darwin || windows

package main

// One unreachable cluster must not hold the others hostage.
//
// The reconcile loop ticks every 300ms and dialled each missing cluster inline,
// with a 15s timeout on the dial. So a cluster whose agent was down, or merely
// slow, parked the whole loop for fifteen seconds: no other cluster got its
// tunnel opened, no dead tunnel got closed, and the ticker's next tick simply
// waited. On a machine running several agents at once - which is the only
// machine this loop exists for - one of them being unreachable froze the rest.

import (
	"sync"
	"testing"
	"time"
)

// A dial that never answers, standing in for an agent that is down.
type slowDialer struct {
	mu      sync.Mutex
	started map[string]int
	release chan struct{}
}

func (d *slowDialer) dial(key string) {
	d.mu.Lock()
	if d.started == nil {
		d.started = map[string]int{}
	}
	d.started[key]++
	d.mu.Unlock()
	<-d.release // never returns until the test says so
}

func (d *slowDialer) count(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started[key]
}

// The property, stated without reference to the implementation: asking to open
// a tunnel for a cluster that does not answer must return promptly, so the next
// cluster is reached in the same tick.
func TestASlowDialDoesNotHoldTheReconcileLoop(t *testing.T) {
	d := &slowDialer{release: make(chan struct{})}
	defer close(d.release)

	inflight := newDialSet()
	start := time.Now()
	for _, key := range []string{"slow.example:2222", "other.example:2222"} {
		k := key
		inflight.begin(k, func() { d.dial(k) })
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("starting two dials took %s: they are not running on their own", el)
	}

	// Both were actually started, and neither waited for the other.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.count("slow.example:2222") == 1 && d.count("other.example:2222") == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("only %d and %d dials started: one cluster is waiting on the other",
		d.count("slow.example:2222"), d.count("other.example:2222"))
}

// And a dial still in flight must not be started again on the next tick. The
// loop ticks three times a second; without this, a cluster that takes fifteen
// seconds to fail would accumulate forty-five concurrent dials.
func TestATickDoesNotRestartADialAlreadyRunning(t *testing.T) {
	d := &slowDialer{release: make(chan struct{})}
	defer close(d.release)

	inflight := newDialSet()
	for i := 0; i < 20; i++ {
		inflight.begin("slow.example:2222", func() { d.dial("slow.example:2222") })
	}

	time.Sleep(200 * time.Millisecond)
	if n := d.count("slow.example:2222"); n != 1 {
		t.Errorf("twenty ticks started %d dials, want 1: a slow cluster piles up", n)
	}
}

// When it finally ends, the cluster is dialable again: the entry must not leak.
func TestADialThatEndsIsForgotten(t *testing.T) {
	d := &slowDialer{release: make(chan struct{})}
	inflight := newDialSet()
	inflight.begin("slow.example:2222", func() { d.dial("slow.example:2222") })

	close(d.release) // the dial returns
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !inflight.running("slow.example:2222") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the cluster is still marked as dialling after its dial returned: it would never be retried")
}
