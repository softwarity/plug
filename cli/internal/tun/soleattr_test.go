//go:build darwin || windows

package tun

import "testing"

// The single-cluster shortcut skipped attribution entirely, and the datapath it
// serves is machine-wide: on macOS the daemon repoints the primary network
// service's resolver, so any process on the box resolves a cluster name and gets
// a fake IP that connects. One cluster up meant a second local account could
// reach another user's postgres by typing "postgres".
func TestASoleClusterIsNotOpenToTheWholeMachine(t *testing.T) {
	const mine, stranger = 501, 502
	owners := map[int]bool{mine: true}

	pidFor := func(uint16) (int, bool) { return 4242, true }
	runAs := func(uid int) func(int) (int, bool) {
		return func(int) (int, bool) { return uid, true }
	}

	if soleAllows(1234, pidFor, runAs(stranger), owners) {
		t.Error("a flow from an account with no client on this cluster was routed into its tunnel")
	}
	if !soleAllows(1234, pidFor, runAs(mine), owners) {
		t.Error("the cluster's own user was refused: this is the main path, it must not regress")
	}
	if !soleAllows(1234, pidFor, runAs(0), owners) {
		t.Error("root was refused; it already owns the machine, and the daemon's own probes run there")
	}
}

// Everything the check cannot answer must behave exactly as it did before the
// check existed. A single-cluster session is plug's main path: a datapath that
// starts refusing on a bad second would be worse than the leak it closes.
func TestWhatTheOwnerCheckCannotAnswerItDoesNotRefuse(t *testing.T) {
	yes := func(uint16) (int, bool) { return 4242, true }
	uid := func(int) (int, bool) { return 502, true }

	// A client too old to record its owner. Unknown is not nobody.
	if !soleAllows(1234, yes, uid, map[int]bool{}) {
		t.Error("no owner recorded was read as no owner allowed: every older client would be cut off")
	}
	// The socket vanished between accept and lookup.
	if !soleAllows(1234, func(uint16) (int, bool) { return 0, false }, uid, map[int]bool{501: true}) {
		t.Error("an unattributable socket was refused on the single-cluster path")
	}
	// The process is gone, or ps failed.
	if !soleAllows(1234, yes, func(int) (int, bool) { return 0, false }, map[int]bool{501: true}) {
		t.Error("an unreadable uid was refused on the single-cluster path")
	}
}

// Several accounts can legitimately hold clients on one cluster: two people on a
// shared build box, or the same person under a second account.
func TestEveryRegisteredAccountIsAllowed(t *testing.T) {
	owners := map[int]bool{501: true, 1001: true}
	pidFor := func(uint16) (int, bool) { return 4242, true }
	for _, uid := range []int{501, 1001} {
		if !soleAllows(1234, pidFor, func(int) (int, bool) { return uid, true }, owners) {
			t.Errorf("uid %d holds a client on this cluster and was refused", uid)
		}
	}
	if soleAllows(1234, pidFor, func(int) (int, bool) { return 999, true }, owners) {
		t.Error("an account holding no client was allowed")
	}
}
