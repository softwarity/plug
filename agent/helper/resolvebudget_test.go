package main

import "testing"

// The invariant that was broken in production, and whose breaking is silent:
// this agent must be able to ANSWER within the window the CLI is willing to
// wait, or the CLI mints a fake address and every verdict here is moot.
//
// The worst case is a lookup that times out AND a witness that times out —
// exactly the case that matters, since it is the one where the answer is not
// "found" and therefore the one the whole check exists for.
func TestTheAgentAnswersBeforeTheClientGivesUp(t *testing.T) {
	worst := resolveLookupBudget + resolveWitnessBudget
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
