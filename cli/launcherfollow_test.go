package main

import (
	"strings"
	"testing"
)

// One rule, every direction: the launcher matches the agent that `plug update`
// was just aimed at. The old policy refused dev builds and downward moves —
// which froze the launcher for good on any machine whose cluster follows the
// main channel, while every other component kept moving.
func TestLauncherFollowsTheAgentEveryDirection(t *testing.T) {
	cases := []struct {
		what          string
		local, remote string
		replace       bool
	}{
		{"same release", "2.8.0", "2.8.0", false},
		{"same dev build, full string", "dev+5b48380", "dev+5b48380", false},
		{"release upgrade", "2.7.3", "2.8.0", true},
		{"release DOWNGRADE — testing an earlier version is legitimate", "2.8.0", "2.7.3", true},
		{"dev launcher, newer dev agent — the frozen-launcher case", "dev+38e1a0e", "dev+5b48380", true},
		{"dev launcher, release agent — coming back to releases", "dev+5b48380", "2.9.0", true},
		{"release launcher, dev agent — joining the main channel", "2.8.0", "dev+5b48380", true},
		{"same x.y.z, different rev (tag rebuilt in place)", "2.8.0+aaa1111", "2.8.0+bbb2222", true},
	}
	for _, c := range cases {
		replace, why := launcherFollow(c.local, c.remote)
		if replace != c.replace {
			t.Errorf("%s: replace = %v, want %v (%s)", c.what, replace, c.replace, why)
		}
		if why == "" {
			t.Errorf("%s: the decision must always say what it did", c.what)
		}
	}
}

// A downgrade is followed, not refused — but it must SAY it is going down and
// how to come back. Silent downgrades are how a stale cluster surprises you.
func TestADowngradeSaysSoAndNamesTheWayBack(t *testing.T) {
	replace, why := launcherFollow("2.8.0", "2.7.3")
	if !replace {
		t.Fatal("a downgrade must be followed")
	}
	if !strings.Contains(why, "DOWN") || !strings.Contains(why, "newer cluster") {
		t.Errorf("downgrade message must announce the direction and the way back, got: %s", why)
	}
}
