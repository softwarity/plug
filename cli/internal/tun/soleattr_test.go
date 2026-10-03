//go:build darwin || windows

package tun

import "testing"

// The single-cluster shortcut skipped attribution entirely, and the datapath it
// serves is machine-wide: on macOS the daemon repoints the primary network
// service's resolver, so any process on the box resolves a cluster name and gets
// a fake IP that connects. One cluster up meant a second local account could
// reach another user's postgres by typing "postgres".
//
// Written with accountA / accountB rather than numbers: the check compares
// accounts as the registry spells them, a uid here and a SID on Windows, and
// the Windows half of this rule did not exist while it compared integers.
func TestASoleClusterIsNotOpenToTheWholeMachine(t *testing.T) {
	owners := map[string]bool{accountA: true}

	pidFor := func(uint16) (int, bool) { return 4242, true }
	runAs := func(account string) func(int) (string, bool) {
		return func(int) (string, bool) { return account, true }
	}

	if soleAllows(1234, pidFor, runAs(accountB), owners) {
		t.Error("a flow from an account with no client on this cluster was routed into its tunnel")
	}
	if !soleAllows(1234, pidFor, runAs(accountA), owners) {
		t.Error("the cluster's own user was refused: this is the main path, it must not regress")
	}
	if !soleAllows(1234, pidFor, runAs(accountAlways), owners) {
		t.Error("root, or LocalSystem, was refused; it already owns the machine, and the daemon's own probes run there")
	}
}

// Everything the check cannot answer must behave exactly as it did before the
// check existed. A single-cluster session is plug's main path: a datapath that
// starts refusing on a bad second would be worse than the leak it closes.
func TestWhatTheOwnerCheckCannotAnswerItDoesNotRefuse(t *testing.T) {
	yes := func(uint16) (int, bool) { return 4242, true }
	account := func(int) (string, bool) { return accountB, true }

	// A client too old to record its owner. Unknown is not nobody.
	if !soleAllows(1234, yes, account, map[string]bool{}) {
		t.Error("no owner recorded was read as no owner allowed: every older client would be cut off")
	}
	// The socket vanished between accept and lookup.
	if !soleAllows(1234, func(uint16) (int, bool) { return 0, false }, account, map[string]bool{accountA: true}) {
		t.Error("an unattributable socket was refused on the single-cluster path")
	}
	// The process is gone, or its token would not open.
	if !soleAllows(1234, yes, func(int) (string, bool) { return "", false }, map[string]bool{accountA: true}) {
		t.Error("an unreadable account was refused on the single-cluster path")
	}
	// An answer that names nobody is not an account, and nobody cannot be refused.
	if !soleAllows(1234, yes, func(int) (string, bool) { return accountNobody, true }, map[string]bool{accountA: true}) {
		t.Error("an identity that names nobody was refused: it cannot hold a cluster, so it cannot be held out of one")
	}
}

// Several accounts can legitimately hold clients on one cluster: two people on a
// shared build box, or the same person under a second account.
func TestEveryRegisteredAccountIsAllowed(t *testing.T) {
	owners := map[string]bool{accountA: true, accountB: true}
	pidFor := func(uint16) (int, bool) { return 4242, true }
	for _, account := range []string{accountA, accountB} {
		if !soleAllows(1234, pidFor, func(int) (string, bool) { return account, true }, owners) {
			t.Errorf("account %s holds a client on this cluster and was refused", account)
		}
	}
	stranger := accountB + "9"
	if soleAllows(1234, pidFor, func(int) (string, bool) { return stranger, true }, owners) {
		t.Error("an account holding no client was allowed")
	}
}
