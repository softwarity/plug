package main

import "testing"

// The one invocation shape: running a command requires naming the process in
// the cluster with -s. Subcommands (ls/test/…) bypass launcherRun, so this rule
// only ever gates the run path.
func TestServeRequired(t *testing.T) {
	for _, empty := range [][]string{nil, {}} {
		if err := serveRequired(empty); err == nil {
			t.Fatalf("a command with no -s must be rejected (exposes=%v)", empty)
		}
	}
	if err := serveRequired([]string{"web:8080:3000"}); err != nil {
		t.Fatalf("one -s must be accepted, got %v", err)
	}
	if err := serveRequired([]string{"a:1:2", "b:3:4"}); err != nil {
		t.Fatalf("multiple -s must be accepted, got %v", err)
	}
}
