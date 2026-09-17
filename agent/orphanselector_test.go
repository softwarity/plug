package agent

import "testing"

// The mark is removed, and ONLY the mark. A takeover on a cluster whose RBAC
// predates the endpoints grant repoints a Service by adding `app: plug` to its
// selector, and the receipt annotation is how that is undone. Lose the receipt -
// a GitOps controller reconciling the object drops what its chart does not
// declare - and the Service points at a pod that is gone: no endpoints, no
// traffic, and nothing able to say what the selector used to be. Removing our
// own mark is the way back, because the fallback only ever added it.
func TestTheAgentsOwnMarkComesOutOfAnOrphanedSelector(t *testing.T) {
	in := map[string]string{
		"app":                        "plug",
		"app.kubernetes.io/instance": "flight-folder-frontend",
		"app.kubernetes.io/name":     "flight-folder-frontend",
	}
	got, had := withoutPlugMark(in)
	if !had {
		t.Fatal("the mark was not recognised")
	}
	if len(got) != 2 || got["app.kubernetes.io/name"] != "flight-folder-frontend" ||
		got["app.kubernetes.io/instance"] != "flight-folder-frontend" {
		t.Fatalf("the workload's own labels did not survive: %v", got)
	}
	if _, still := got["app"]; still {
		t.Fatal("the mark is still there")
	}
	if in["app"] != "plug" {
		t.Fatal("the caller's map was mutated; the receipt is written from it")
	}
}

// A selector that is ONLY the mark is left alone, and this is the guard that
// matters most: the agent's own Service selects `app: plug` and nothing else.
// Stripping it there would leave an empty selector, which matches EVERY pod in
// the namespace instead of none - turning a repair into an outage.
func TestASelectorThatIsOnlyTheMarkIsLeftAlone(t *testing.T) {
	in := map[string]string{"app": "plug"}
	got, had := withoutPlugMark(in)
	if had {
		t.Fatal("the agent's own Service was treated as an orphaned takeover")
	}
	if len(got) != 1 || got["app"] != "plug" {
		t.Fatalf("it was modified anyway: %v", got)
	}
}

// Everything else is none of our business: a Service we never touched, and one
// whose `app` names something other than this agent.
func TestSelectorsThatAreNotOursAreUntouched(t *testing.T) {
	for _, in := range []map[string]string{
		{"app.kubernetes.io/name": "httpbin"},
		{"app": "postgres", "tier": "db"},
		{},
		nil,
	} {
		if got, had := withoutPlugMark(in); had {
			t.Fatalf("%v was mistaken for an orphaned takeover, giving %v", in, got)
		}
	}
}
