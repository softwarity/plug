//go:build darwin || windows

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

type stopCount struct{ n atomic.Int32 }

func (s *stopCount) Stop() { s.n.Add(1) }

// A plug that registers while the daemon decides to stop calls the stop off:
// the marker goes up first, the clients are counted again, and one found there
// keeps the datapath running and the marker down.
func TestAPlugArrivingAsTheDaemonDecidesToStopKeepsItRunning(t *testing.T) {
	dp := &stopCount{}
	stop := make(chan struct{})
	tick := make(chan time.Time)
	var marked atomic.Bool
	var arrived atomic.Bool
	clients := func() int {
		if marked.Load() {
			arrived.Store(true) // registered just as the marker went up
		}
		if arrived.Load() {
			return 1
		}
		return 0
	}
	done := make(chan struct{})
	go func() {
		reapWhenIdle(dp, stop, tick, 30*time.Second, clients, func(on bool) { marked.Store(on) })
		close(done)
	}()
	t0 := time.Now()
	tick <- t0
	tick <- t0.Add(31 * time.Second)
	tick <- t0.Add(32 * time.Second)
	close(stop)
	<-done
	if dp.n.Load() != 0 {
		t.Fatal("the datapath was stopped under a plug that had just registered")
	}
	if marked.Load() {
		t.Fatal("the stop was called off and the stopping marker left up: every launcher would wait for nothing")
	}
}

// With nobody arriving, the datapath stops after the grace with the marker up,
// and not before.
func TestAnIdleDaemonStopsAfterItsGraceWithTheMarkerUp(t *testing.T) {
	dp := &stopCount{}
	stop := make(chan struct{})
	tick := make(chan time.Time)
	var marked atomic.Bool
	done := make(chan struct{})
	go func() {
		reapWhenIdle(dp, stop, tick, 30*time.Second, func() int { return 0 }, func(on bool) { marked.Store(on) })
		close(done)
	}()
	t0 := time.Now()
	tick <- t0
	tick <- t0.Add(20 * time.Second)
	if dp.n.Load() != 0 || marked.Load() {
		t.Fatal("stopped, or marked stopping, inside the grace")
	}
	tick <- t0.Add(30 * time.Second)
	<-done
	if dp.n.Load() != 1 || !marked.Load() {
		t.Fatalf("after the grace: stops=%d marked=%v, want 1 and true", dp.n.Load(), marked.Load())
	}
}

// A launcher waits while the marker is up, and no longer than its bound.
func TestALauncherWaitsForAStoppingDaemonToBeGone(t *testing.T) {
	var calls atomic.Int32
	start := time.Now()
	awaitStoppedDaemon(func() bool { return calls.Add(1) <= 3 }, 5*time.Second)
	if calls.Load() < 4 {
		t.Fatalf("stopped waiting while the marker was still up (%d looks)", calls.Load())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("waited %s after the marker went down", took)
	}
	start = time.Now()
	awaitStoppedDaemon(func() bool { return true }, 300*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a marker that never goes down held the launcher %s against a 300ms bound", took)
	}
}
