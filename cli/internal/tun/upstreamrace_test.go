package tun

import (
	"sync/atomic"
	"testing"
	"time"
)

// The budget is four seconds per server, and they used to be asked strictly in
// turn: a single unreachable resolver, which is what a VPN transition leaves
// behind, made EVERY dotted lookup take four seconds, and two of them eight.
// Client libraries give up well before that, so a machine whose primary resolver
// had gone quiet looked like a cluster that did not answer.
func TestADeadFirstResolverDoesNotCostItsWholeTimeout(t *testing.T) {
	var asked atomic.Int32
	start := time.Now()
	got := raceUpstreams([]string{"dead", "alive"}, 50*time.Millisecond, func(addr string) []byte {
		asked.Add(1)
		if addr == "dead" {
			time.Sleep(4 * time.Second) // the real budget, never waited out
			return nil
		}
		return []byte("an answer")
	})
	took := time.Since(start)

	if string(got) != "an answer" {
		t.Fatalf("got %q, want the healthy server's reply", got)
	}
	if took > 2*time.Second {
		t.Errorf("waited %v on a dead first server; the second one was reachable the whole time", took)
	}
	if asked.Load() != 2 {
		t.Errorf("asked %d servers, want both: the point is not to wait for the first to fail", asked.Load())
	}
}

// The healthy case must stay one question. Asking everyone every time would
// triple the traffic a resolver sees, for a case that is not happening.
func TestAHealthyFirstResolverIsAskedAlone(t *testing.T) {
	var asked atomic.Int32
	got := raceUpstreams([]string{"a", "b", "c"}, time.Second, func(string) []byte {
		asked.Add(1)
		return []byte("quick")
	})
	if string(got) != "quick" {
		t.Fatalf("got %q", got)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("asked %d servers when the first answered at once; the others were never needed", n)
	}
}

// A server that says no, rather than being slow, must not hold the next one
// behind a stagger it no longer shares.
func TestARefusalBringsTheNextServerInAtOnce(t *testing.T) {
	start := time.Now()
	got := raceUpstreams([]string{"no", "yes"}, 5*time.Second, func(addr string) []byte {
		if addr == "no" {
			return nil
		}
		return []byte("yes")
	})
	if string(got) != "yes" {
		t.Fatalf("got %q, want the second server's reply", got)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("waited %v after an immediate refusal; the stagger is for a SLOW server, not a "+
			"refusing one", took)
	}
}

// Everyone refusing is an answer of its own: no record, rather than a hang.
func TestAllRefusingReturnsNothing(t *testing.T) {
	done := make(chan []byte)
	go func() {
		done <- raceUpstreams([]string{"a", "b"}, 10*time.Millisecond, func(string) []byte { return nil })
	}()
	select {
	case r := <-done:
		if r != nil {
			t.Errorf("got %q from servers that all refused", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("every server refused and the relay never returned")
	}
}

func TestNoServersIsNotACrash(t *testing.T) {
	if r := raceUpstreams(nil, time.Second, func(string) []byte { return []byte("x") }); r != nil {
		t.Errorf("got %q with no servers configured", r)
	}
}
