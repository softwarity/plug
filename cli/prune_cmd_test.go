package main

import (
	"strings"
	"testing"
)

// The one decision that must never be wrong in the dangerous direction: with no
// agent answering, EVERY cached core would qualify as unused, and "my laptop is
// offline" would read as "delete everything". Refuse instead.
func TestPruneRefusesWhenNoAgentAnswered(t *testing.T) {
	victims, refusal := pruneVictims([]string{"2.7.3", "2.8.0", "dev+abc1234"}, map[string]bool{})
	if refusal == "" {
		t.Fatal("an empty active set pruned instead of refusing")
	}
	if len(victims) != 0 {
		t.Fatalf("refused but still named victims: %v", victims)
	}
}

func TestPruneKeepsWhatAgentsRunAndDropsTheRest(t *testing.T) {
	cached := []string{"2.5.4", "2.7.0", "2.7.3", "2.8.0", "dev", "dev+9f2a1c", "dev+e57a0ad"}
	active := map[string]bool{
		"2.8.0":      true, // the main cluster
		"dev+9f2a1c": true, // a bench cluster on a branch build
	}
	victims, refusal := pruneVictims(cached, active)
	if refusal != "" {
		t.Fatalf("refused with agents answering: %s", refusal)
	}
	got := strings.Join(victims, ",")
	want := "2.5.4,2.7.0,2.7.3,dev,dev+e57a0ad"
	if got != want {
		t.Errorf("victims = %s, want %s", got, want)
	}
}

// An agent may run a version that is not cached here (never connected since its
// update) — that must not confuse the decision, and a fully-active cache prunes
// nothing.
func TestPruneWithNothingToDo(t *testing.T) {
	victims, refusal := pruneVictims([]string{"2.8.0"}, map[string]bool{"2.8.0": true, "2.9.0": true})
	if refusal != "" || len(victims) != 0 {
		t.Errorf("victims = %v, refusal = %q; want none and none", victims, refusal)
	}
}

// Version strings are matched exactly: an agent on 2.8.0 does not protect
// dev+2.8.0 or any other lookalike. Deleting a lookalike is safe (it re-downloads),
// keeping one silently would let the cache grow back for ever.
func TestPruneMatchesVersionsExactly(t *testing.T) {
	victims, _ := pruneVictims([]string{"2.8.0", "dev+abc"}, map[string]bool{"2.8.0": true})
	if len(victims) != 1 || victims[0] != "dev+abc" {
		t.Errorf("victims = %v, want exactly [dev+abc]", victims)
	}
}
