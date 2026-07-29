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
