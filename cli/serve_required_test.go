package main

import "testing"

// The invocation shape: a command either serves a name (-s) or declares itself
// a pure client (-c) — one or the other, never both, never neither. Subcommands
// (ls/test/…) bypass launcherRun, so this rule only ever gates the run path.
func TestServeRequired(t *testing.T) {
	for _, empty := range [][]string{nil, {}} {
		if err := serveRequired(empty, false, false); err == nil {
			t.Fatalf("a command with neither -s nor -c must be rejected (exposes=%v)", empty)
		}
	}
	if err := serveRequired([]string{"web:8080:3000"}, false, false); err != nil {
		t.Fatalf("one -s must be accepted, got %v", err)
	}
	if err := serveRequired([]string{"a:1:2", "b:3:4"}, false, false); err != nil {
		t.Fatalf("multiple -s must be accepted, got %v", err)
	}
	if err := serveRequired(nil, true, false); err != nil {
		t.Fatalf("-c alone must be accepted, got %v", err)
	}
	if err := serveRequired([]string{"web:8080:3000"}, true, false); err == nil {
		t.Fatal("-s and -c together must be rejected")
	}
}

// The cluster name must be a valid DNS label (RFC 1035, leading letter) — the
// same rule the agent enforces, checked client-side so a typo fails instantly.
func TestParseExposeName(t *testing.T) {
	good := []string{"my-app:8080:3000", "a:1:2", "web1:80:80", "svc-2-x:5000:5000"}
	for _, s := range good {
		if _, err := parseExpose(s); err != nil {
			t.Errorf("valid spec %q rejected: %v", s, err)
		}
	}
	bad := []string{
		"my_app:8080:3000", // underscore — the exact CI trap
		"1web:80:80",       // leading digit (k8s rejects)
		"-web:80:80",       // leading hyphen
		"web-:80:80",       // trailing hyphen
		"Web:80:80",        // uppercase
		"a.b:80:80",        // dot is a separator, not a label char
	}
	for _, s := range bad {
		if _, err := parseExpose(s); err == nil {
			t.Errorf("invalid name in %q accepted", s)
		}
	}
}
