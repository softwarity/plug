package main

import (
	"reflect"
	"sort"
	"testing"
)

// The cluster wins by default, overwriting an inherited value: a plugged process
// behaves as it does inside the cluster, not as the shell left it. --no-env=KEY
// is the escape - it holds a key back to the caller's own value (a test db).
func TestTheClusterWinsUnlessNoEnvHoldsAKeyBack(t *testing.T) {
	cluster := []string{"APP_DB_HOST=odb", "APP_DB_PASSWORD=from-cluster", "APP_MONGODB_HOST=mongodb"}
	caller := []string{"PATH=/usr/bin", "APP_DB_PASSWORD=from-shell"}

	// Default: the cluster's password overwrites the shell's; nothing is kept.
	set, kept := mergeWorkloadEnv(cluster, caller, envPolicy{})
	if set["APP_DB_PASSWORD"] != "from-cluster" {
		t.Fatalf("the cluster's value must win, got %q", set["APP_DB_PASSWORD"])
	}
	if set["APP_DB_HOST"] != "odb" || set["APP_MONGODB_HOST"] != "mongodb" || len(kept) != 0 {
		t.Fatalf("set=%v kept=%v", set, kept)
	}

	// --no-env=APP_DB_PASSWORD: the cluster holds that key back, the shell's value
	// stands, and the key is named in kept.
	set, kept = mergeWorkloadEnv(cluster, caller, parseNoEnv("APP_DB_PASSWORD"))
	if _, overwritten := set["APP_DB_PASSWORD"]; overwritten {
		t.Fatal("--no-env=APP_DB_PASSWORD must NOT take the cluster's value")
	}
	if !reflect.DeepEqual(kept, []string{"APP_DB_PASSWORD"}) {
		t.Fatalf("kept = %v, want the held-back key named", kept)
	}
	if set["APP_DB_HOST"] != "odb" {
		t.Fatalf("the other keys must still come from the cluster: %v", set)
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

// A value that came through empty is named, not counted as delivered: on a
// cluster without pods/exec a Secret-backed variable arrives as "" from the
// pod spec, and the service fails on a missing password with no hint of RBAC.
func TestEmptyValuesAreNamedBesideTheCount(t *testing.T) {
	set, _, empty := mergeWorkloadEnvWithEmpty([]string{"APP_DB_HOST=odb", "APP_DB_PASSWORD=", "APP_DB_USER="}, nil, envPolicy{})
	if len(set) != 3 {
		t.Fatalf("set = %v", set)
	}
	if !reflect.DeepEqual(empty, []string{"APP_DB_PASSWORD", "APP_DB_USER"}) {
		t.Fatalf("empty = %v", empty)
	}
}
