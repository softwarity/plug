package main

import "testing"

// A release tag already designates one commit, so its build metadata is noise;
// a branch build without its revision would name every build of every branch.
func TestShortVersion(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2.4.0+983761c", "2.4.0"},
		{"2.3.0+bb03611", "2.3.0"},
		{"2.0.0+20015cd", "2.0.0"},
		{"2.4", "2.4"},
		{"2.4+abc1234", "2.4"},
		{"2.4.1", "2.4.1"},             // already bare (2.4.1 and later)
		{"dev+1ca6a07", "dev+1ca6a07"}, // the revision IS the identity here
		{"dev", "dev"},
		{"", ""},
	} {
		if got := shortVersion(c.in); got != c.want {
			t.Errorf("shortVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The agent's answer becomes a directory name under ~/.plug/versions, and then
// an executable plug runs — as root on macOS. Only this charset gets through.
func TestSafeVersionRe(t *testing.T) {
	for _, ok := range []string{"2.5.4", "dev", "dev+9f2a1c", "2.4.0+983761c", "v2.5.4", "2"} {
		if !safeVersionRe.MatchString(ok) {
			t.Errorf("safeVersionRe rejected a real version: %q", ok)
		}
	}
	for _, bad := range []string{
		"../../../../Library/LaunchDaemons/x", "..", "a/b", `a\b`, "", "2.5.4 ; rm -rf /",
		"2.5.4\n", "$(whoami)", "-rf", strings_repeat("9", 65),
	} {
		if safeVersionRe.MatchString(bad) {
			t.Errorf("safeVersionRe accepted %q", bad)
		}
	}
}

func strings_repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
