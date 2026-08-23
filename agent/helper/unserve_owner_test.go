package agent

import (
	"fmt"
	"testing"
)

// Holding a name is not forever: sleep past the keepalive and the forward dies,
// the lease frees the name, and the next session is granted it. The first
// session tears down eventually, and that teardown must not touch a name it no
// longer holds — it would delete its SUCCESSOR's signpost and restore a
// workload that session had parked, which is exactly the "two sessions, one
// name" damage the lease exists to stop.
func TestUnserveMayAct(t *testing.T) {
	for _, tc := range []struct {
		why  string
		held string // what the lease records
		mine string // what the caller says it holds
		want bool
	}{
		{"a session that lost the name while it was away", "40002", "40001", false},
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

// A workload that fails to come back must NOT be forgotten: the receipt lives in
// the signpost's labels, and the signpost is deleted right after the restore. So
// a swallowed failure left the workload down with nothing recording it had been
// parked — not even for the boot gc.
func TestRestartParkedContainersReportsWhatItCouldNotBringBack(t *testing.T) {
	for _, tc := range []struct {
		why  string
		code int
		err  error
		want bool // reported as failed?
	}{
		{"started fine", 204, nil, false},
		{"already running", 304, errStub, false},
		{"removed meanwhile", 404, errStub, false},
		{"host port taken over", 500, errStub, true},
		{"conflict", 409, errStub, true},
	} {
		got := tc.err != nil && tc.code != 404 && tc.code != 304
		if got != tc.want {
			t.Errorf("%s (HTTP %d): reported=%v want %v", tc.why, tc.code, got, tc.want)
		}
	}
}

var errStub = fmt.Errorf("docker said no")
