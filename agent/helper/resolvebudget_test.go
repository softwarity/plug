package main

import "testing"

// The invariant that was broken in production, and whose breaking is silent:
// this agent must be able to ANSWER within the window the CLI is willing to
// wait, or the CLI mints a fake address and every verdict here is moot.
//
// The worst case is a lookup that times out AND a witness that times out —
// exactly the case that matters, since it is the one where the answer is not
// "found" and therefore the one the whole check exists for.
//
// The two run SIDE BY SIDE: the witness starts with the lookup rather than after
// it, so the worst case is the LONGER of the two and not their sum. That is what
// paid for the witness's larger budget, which Kubernetes needed — its witness is
// answered by CoreDNS over the network, where Docker's is answered by the daemon
// from memory.
//
// Both are asserted on purpose. The first is the invariant as the code stands.
// The second is the net under it: if the parallelism is ever undone, the budgets
// must STILL fit inside the client's patience — tightly, but without the silent
// mint that a cascade caused the last time.
func TestTheAgentAnswersBeforeTheClientGivesUp(t *testing.T) {
	worst := resolveLookupBudget
	if resolveWitnessBudget > worst {
		worst = resolveWitnessBudget
	}
	if worst >= cliResolveBudget {
		t.Fatalf("worst-case answer takes %s but the CLI gives up at %s — it will mint before we speak "+
			"(this is the 3s+2s vs 3s bug: the agent could never answer in time)", worst, cliResolveBudget)
	}
	// Not merely under it: a slow link, a busy runner and the SSH round trip all
	// sit between the two. Keep real headroom rather than a photo finish.
	if margin := cliResolveBudget - worst; margin < cliResolveBudget/3 {
		t.Errorf("only %s of margin against the CLI's %s — too tight once the answer has to travel back",
			margin, cliResolveBudget)
	}
	if seq := resolveLookupBudget + resolveWitnessBudget; seq >= cliResolveBudget {
		t.Errorf("run one after the other these budgets take %s, past the CLI's %s — the parallelism is now "+
			"load-bearing, and nothing in the type system says so", seq, cliResolveBudget)
	}
}

// The lookup budget has to stay comfortably above what a real cluster name
// costs (the embedded resolver answers from memory) and well below the client's
// patience. Too low and a healthy cluster reads as absent; too high and we are
// back to answering after the mint.
func TestTheLookupBudgetIsBetweenAMemoryLookupAndTheClientsPatience(t *testing.T) {
	if resolveLookupBudget < 200*1000*1000 { // 200ms
		t.Errorf("resolveLookupBudget = %s — a busy embedded resolver would read as an absent name", resolveLookupBudget)
	}
	if resolveLookupBudget >= cliResolveBudget {
		t.Errorf("resolveLookupBudget = %s exceeds the CLI's %s on its own", resolveLookupBudget, cliResolveBudget)
	}
}
