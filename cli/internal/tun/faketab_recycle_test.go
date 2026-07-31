package tun

import (
	"fmt"
	"testing"
	"time"
)

// The /24 used to be a one-way street: 254 names and every later one got
// NXDOMAIN for ever. macOS routes EVERY single-label lookup to this table, and a
// browser's anti-hijack probes alone are three random names per network change,
// so a daemon that lives for days reached the end and stayed there — cluster
// names included, until it was restarted.
func TestFaketabRecyclesTheColdestFakeWhenFull(t *testing.T) {
	tab := newFaketab(0xC6120000) // 198.18.0.0

	var first uint32
	for i := 0; i < 253; i++ {
		ip := tab.mint(fmt.Sprintf("probe-%d", i))
		if ip == 0 {
			t.Fatalf("subnet exhausted after only %d names", i)
		}
		if i == 0 {
			first = ip
		}
	}

	// Everything was just minted, so nothing is past reuseFloor: refusing is the
	// honest answer rather than stealing an address a client may still hold.
	if ip := tab.mint("one-too-many"); ip != 0 {
		t.Errorf("a full subnet handed out %d while every fake was in recent use", ip)
	}

	// Age the first entry past the floor — now it is fair game.
	tab.mu.Lock()
	tab.seen[first] = time.Now().Add(-2 * reuseFloor)
	tab.mu.Unlock()

	got := tab.mint("recycled")
	if got != first {
		t.Fatalf("mint reused %d, want the coldest fake %d", got, first)
	}
	if n, ok := tab.lookup(got); !ok || n != "recycled" {
		t.Errorf("lookup(%d) = (%q,%v), want the new owner", got, n, ok)
	}
	// And the old owner is gone rather than aliased onto the same address.
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.byIP[first] != "recycled" {
		t.Errorf("byIP[%d] = %q, want the new name", first, tab.byIP[first])
	}
}

// Dialling a fake is what proves it is still wanted, so it must push the entry
// out of reach of the recycler.
func TestLookupKeepsAFakeAlive(t *testing.T) {
	tab := newFaketab(0xC6120000)
	ip := tab.mint("busy")

	tab.mu.Lock()
	tab.seen[ip] = time.Now().Add(-2 * reuseFloor)
	tab.mu.Unlock()

	tab.lookup(ip) // a connect to it, right now

	tab.mu.Lock()
	defer tab.mu.Unlock()
	if got := tab.coldest(time.Now()); got == ip {
		t.Error("a fake dialled moments ago was offered up for recycling")
	}
}
