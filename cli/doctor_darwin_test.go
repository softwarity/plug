//go:build darwin

package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The measurement that shipped as a false positive: 3.03s, then 198.18.0.5, on a
// perfectly current datapath. doctor blamed the version and offered a remedy
// ("relaunch, the new core takes over") that cannot work — the datapath WAS the
// new core. It has to name the timeout, and the loop that causes it.
func TestASlowMintIsTheExistenceCheckTimingOut(t *testing.T) {
	c := nxdomainVerdict("198.18.0.5", nil, 3030*time.Millisecond, 1)

	if c.status != stWarn {
		t.Errorf("status = %v, want a warning", c.status)
	}
	if !strings.Contains(c.detail, "timed out") {
		t.Errorf("detail = %q, want it to name the timeout", c.detail)
	}
	if !strings.Contains(c.detail, "3s") {
		t.Errorf("detail = %q, want the measured duration — it IS the diagnosis", c.detail)
	}
	if !strings.Contains(c.remedy, "Docker Engine") || !strings.Contains(c.remedy, "dns") {
		t.Errorf("remedy = %q, want the Docker Desktop DNS pin", c.remedy)
	}
	// The bug in one assertion: never blame the version on this evidence again.
	for _, wrong := range []string{"predates", "relaunch — the new core"} {
		if strings.Contains(c.detail+c.remedy, wrong) {
			t.Errorf("still blaming the version (%q) on a timeout: %q / %q", wrong, c.detail, c.remedy)
		}
	}
}

// An IMMEDIATE mint is the other cause: the datapath never checked. The remedy
// is to close EVERY session — the daemon stops on its own 30s later and the next
// launch takes the current core. Not `plug down`, which strands them instead.
func TestAnImmediateMintTellsYouToCloseYourSessions(t *testing.T) {
	c := nxdomainVerdict("198.18.0.5", nil, 12*time.Millisecond, 2)

	if c.status != stWarn {
		t.Errorf("status = %v, want a warning", c.status)
	}
	// NOT `plug down`: it strands the very sessions it asks about. Closing them
	// is the whole fix — the daemon stops on its own 30s later.
	if strings.Contains(c.remedy, "plug down") {
		t.Errorf("remedy = %q — plug down strands running sessions; closing them is the fix", c.remedy)
	}
	if !strings.Contains(c.remedy, "close ALL") {
		t.Errorf("remedy = %q, want it to say close ALL sessions", c.remedy)
	}
	if !strings.Contains(c.remedy, "2 still open") {
		t.Errorf("remedy = %q, want the session COUNT — not knowing one was left open is the whole trap", c.remedy)
	}
	if strings.Contains(c.remedy, "Docker Engine") {
		t.Errorf("remedy = %q, that is the timeout's remedy, not this one", c.remedy)
	}
}

// The two causes must never produce the same advice: that is the whole point of
// measuring, and the regression to guard.
func TestTheTwoCausesGiveDifferentRemedies(t *testing.T) {
	slow := nxdomainVerdict("198.18.0.5", nil, 3*time.Second, 1)
	fast := nxdomainVerdict("198.18.0.5", nil, time.Millisecond, 1)
	if slow.remedy == fast.remedy {
		t.Errorf("both causes advise %q — the measurement is being ignored", slow.remedy)
	}
}

func TestACleanNXDOMAINIsHealthy(t *testing.T) {
	c := nxdomainVerdict("", errors.New("no such host"), 8*time.Millisecond, 1)
	if c.status != stOK {
		t.Errorf("status = %v (%s), want OK — this is the working case", c.status, c.detail)
	}
}

// Not resolving is only good news when it is FAST. A lookup that fails after
// three seconds is a path that times out, and reporting OK would bury exactly
// what this check exists to surface.
func TestASlowFailureIsNotHealth(t *testing.T) {
	c := nxdomainVerdict("", errors.New("no such host"), 3*time.Second, 1)
	if c.status != stWarn {
		t.Errorf("status = %v, want a warning: %s", c.status, c.detail)
	}
	if !strings.Contains(c.detail, "timing out") {
		t.Errorf("detail = %q, want it to name the timeout", c.detail)
	}
}

// A wildcard resolver, or a machine where that name genuinely exists, proves
// nothing either way — and must not be reported as a plug fault.
func TestAnAnswerThatIsNotOursProvesNothing(t *testing.T) {
	c := nxdomainVerdict("93.184.216.34", nil, 5*time.Millisecond, 1)
	if c.status != stOK {
		t.Errorf("status = %v, want OK — the address is not in plug's range: %s", c.status, c.detail)
	}
	if !strings.Contains(c.detail, "not conclusive") {
		t.Errorf("detail = %q, want it to say the probe is inconclusive", c.detail)
	}
}

// The threshold has to sit below the CLI's 3s give-up and far above a real
// answer, or one of the two causes gets diagnosed as the other.
func TestTheStallThresholdSitsBetweenTheTwoCases(t *testing.T) {
	if probeStall >= 3*time.Second {
		t.Errorf("probeStall = %s, but the CLI gives up at 3s — a timeout would read as an immediate mint", probeStall)
	}
	if probeStall < 500*time.Millisecond {
		t.Errorf("probeStall = %s — a merely slow answer would read as a timeout", probeStall)
	}
}

// What the doctor prints beside a running daemon: "core v2.13.2" or, failing
// that, the raw path. A wrong answer is therefore a wrong VERSION shown as fact,
// next to a pid that is real, which is the shape of mistake nobody double-checks.
//
// The last case is why this scans from the right. The store is ~/.plug/versions,
// so the last "versions" in the path is the one that means something; an earlier
// one belongs to whoever owns the home directory.
func TestVersionFromCorePath(t *testing.T) {
	for _, c := range []struct{ why, path, want string }{
		{"the ordinary cached core", "/Users/dev/.plug/versions/2.13.2/plug", "2.13.2"},
		{"a dev build, which is a version like any other", "/Users/dev/.plug/versions/dev+abc1234/plug", "dev+abc1234"},
		{"a home directory that also says versions", "/Users/versions/.plug/versions/2.13.2/plug", "2.13.2"},
		{"the installed launcher, which is not a cached core", "/usr/local/bin/plug", ""},
		{"nothing after the marker", "/Users/dev/.plug/versions", ""},
		{"empty", "", ""},
	} {
		if got := versionFromCorePath(c.path); got != c.want {
			t.Errorf("versionFromCorePath(%q) = %q, want %q (%s)", c.path, got, c.want, c.why)
		}
	}
}
