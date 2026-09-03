//go:build darwin || windows

package tun

import "testing"

// The rule "one cluster, one account" rests entirely on two questions each
// platform answers its own way: who am I, and can that hold a cluster. This pins
// both, because a platform that quietly answers "nobody" to the first disables
// the rule without failing anything.
//
// That is not hypothetical: it is exactly what Windows did until this code. Every
// process there reports uid -1, so every client recorded the same owner, no
// account could be told from another, and a second session on the machine reached
// a cluster somebody else had opened with their key.
func TestAnAccountIsSomethingOnEveryPlatform(t *testing.T) {
	me := thisAccount()
	if me == "" {
		t.Fatal("this platform cannot say who is running, so the rule that gives a cluster to one " +
			"account has nothing to compare and silently protects nothing")
	}
	if !accountHolds(me) && !accountHolds(accountA) {
		t.Fatalf("neither the running account (%q) nor a sample one can hold a cluster: the rule is off", me)
	}
	if accountHolds(accountAlways) {
		t.Errorf("%q owns the machine already; letting it hold a cluster would refuse the daemon's own work", accountAlways)
	}
	if accountHolds(accountNobody) {
		t.Errorf("%q names nobody and must not hold a cluster against anybody", accountNobody)
	}
	if accountHolds("") {
		t.Error("an identity that could not be read must not hold a cluster")
	}
	if !accountHolds(accountA) || !accountHolds(accountB) || accountA == accountB {
		t.Error("two distinct real accounts must both be able to hold, and must not be equal")
	}
}
