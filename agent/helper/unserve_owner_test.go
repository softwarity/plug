package main

import "testing"

// A session displaced by `force` keeps running and tears down eventually. That
// teardown must not touch the name it no longer holds — otherwise it deletes
// its SUCCESSOR's signpost and restores a workload the successor had parked,
// which is exactly the "two sessions, one name" damage the lease exists to
// stop. --force would have reintroduced it through the back door.
func TestUnserveMayAct(t *testing.T) {
	for _, tc := range []struct {
		why  string
		held string // what the lease records
		mine string // what the caller says it holds
		want bool
	}{
		{"the displaced session, on the port it used to hold", "40002", "40001", false},
		{"the session that really holds it", "40002", "40002", true},
		{"a caller too old to name its port", "40002", "", true},
		{"no lease recorded: nothing to arbitrate", "", "40001", true},
		{"neither side knows anything", "", "", true},
	} {
		if got := unserveMayAct(tc.held, tc.mine); got != tc.want {
			t.Errorf("%s: unserveMayAct(held=%q, mine=%q) = %v, want %v",
				tc.why, tc.held, tc.mine, got, tc.want)
		}
	}
}
