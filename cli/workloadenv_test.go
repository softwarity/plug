package main

import (
	"reflect"
	"sort"
	"testing"
)

// The caller's variables win. A plugged service must be pointable at a test
// database from the shell, whatever the cluster says; and the credentials the
// cluster has must reach a process that does not carry them itself.
func TestTheCallerWinsAndTheClusterFillsTheRest(t *testing.T) {
	cluster := []string{"NEO_ODB_HOST=odb", "NEO_ODB_PASSWORD=from-cluster", "NEO_MONGODB_HOST=mongodb"}
	caller := []string{"PATH=/usr/bin", "NEO_ODB_PASSWORD=from-shell"}
	set, kept := mergeWorkloadEnv(cluster, caller, envPolicy{})
	if set["NEO_ODB_HOST"] != "odb" || set["NEO_MONGODB_HOST"] != "mongodb" {
		t.Fatalf("the cluster's variables did not come through: %v", set)
	}
	if _, overridden := set["NEO_ODB_PASSWORD"]; overridden {
		t.Fatal("the cluster's password overrode the shell's")
	}
	if !reflect.DeepEqual(kept, []string{"NEO_ODB_PASSWORD"}) {
		t.Fatalf("kept = %v, want the caller's own key named", kept)
	}
}

// --no-env alone turns projection off entirely; --no-env A,B drops those keys
// and projects the rest. Both spellings must parse, spaces and all.
func TestNoEnvTurnsItOffOrDropsNamedKeys(t *testing.T) {
	cluster := []string{"A=1", "B=2", "C=3"}
	if set, _ := mergeWorkloadEnv(cluster, nil, parseNoEnv("")); len(set) != 0 {
		t.Fatalf("a bare --no-env must project nothing, got %v", set)
	}
	set, _ := mergeWorkloadEnv(cluster, nil, parseNoEnv("A, C"))
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"B"}) {
		t.Fatalf("--no-env A,C must leave only B, got %v", keys)
	}
}

// Malformed lines are dropped rather than invented, and a value may contain "=".
func TestWorkloadLinesAreReadVerbatim(t *testing.T) {
	set, _ := mergeWorkloadEnv([]string{"URL=postgres://u:p@odb/db?x=1", "garbage", "=novalue"}, nil, envPolicy{})
	if set["URL"] != "postgres://u:p@odb/db?x=1" || len(set) != 1 {
		t.Fatalf("got %v", set)
	}
}
