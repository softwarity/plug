package tun

import (
	"os"
	"testing"
)

// mustWorkInCI turns "this environment cannot answer" into a failure on a
// runner, and leaves it a skip on a developer's machine.
//
// The tests below reach for the OS primitives that decide WHICH CLUSTER an
// intercepted flow belongs to: the process table through ps, the socket table
// through lsof. Every one of them skipped when the primitive said no, and go test
// prints nothing about a skip unless asked. So forcing procStart and ppidOf to
// report failure, which is the whole attribution mechanism broken, left the
// package green and silent. That was measured, not supposed.
//
// A CI runner has ps and lsof. There, an unanswerable question is a broken
// build, and this is the same shape route_windows_test.go already uses for the
// elevation it needs.
func mustWorkInCI(t *testing.T, ok bool, what string) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s answered nothing on a CI runner, where it is guaranteed. "+
			"That is the attribution mechanism failing, not an environment limitation: "+
			"a flow can no longer be traced to the cluster that should receive it", what)
	}
	t.Skipf("%s unavailable in this environment", what)
}
