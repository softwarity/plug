package main

import "testing"

// The trailing `force` is optional, and getting its arithmetic wrong fails in
// both directions: too strict and every session breaks, too loose and a caller
// that never asked for it displaces a colleague's name.
func TestServeNameShape(t *testing.T) {
	for _, tc := range []struct {
		why       string
		cmd       []string
		wantForce bool
		wantOK    bool
	}{
		{"the ordinary form", []string{"serve-name", "web", "80:40001", "takeover"}, false, true},
		{"multi-port, ordinary", []string{"serve-name", "web", "80:40001,25:40002", "takeover"}, false, true},
		{"forced", []string{"serve-name", "web", "80:40001", "takeover", "force"}, true, true},
		{"missing takeover", []string{"serve-name", "web", "80:40001"}, false, false},
		{"takeover misspelt", []string{"serve-name", "web", "80:40001", "takeovr"}, false, false},
		{"a 5th word that is not force", []string{"serve-name", "web", "80:40001", "takeover", "please"}, false, false},
		{"one word too many", []string{"serve-name", "web", "80:40001", "takeover", "force", "now"}, false, false},
		{"nothing at all", []string{"serve-name"}, false, false},
		{"empty", nil, false, false},
	} {
		force, ok := serveNameShape(tc.cmd)
		if ok != tc.wantOK || force != tc.wantForce {
			t.Errorf("%s: serveNameShape(%q) = (force=%v, ok=%v), want (force=%v, ok=%v)",
				tc.why, tc.cmd, force, ok, tc.wantForce, tc.wantOK)
		}
	}
}
