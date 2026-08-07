package main

import (
	"strings"
	"testing"
)

// THE regression to hold, and it has already happened twice: telling someone to
// run `plug down` in order to pick up a new version. It strands the running
// sessions it is asking about — their datapath disappears while the processes
// keep going — and it is never the answer, because the daemon stops by itself
// 30s after the last session closes.
//
// Every user-facing wording produced on the update path is swept here, so a
// future edit that reintroduces the advice fails before anyone lives it.
func TestNoUpdateAdviceEverSendsYouToPlugDown(t *testing.T) {
	for _, m := range []string{
		datapathLagMessage(0), datapathLagMessage(1), datapathLagMessage(7),
		downStrandsSessions(0), downStrandsSessions(1), downStrandsSessions(7),
	} {
		if strings.Contains(m, "plug down") {
			t.Errorf("advice sends the user to `plug down`: %q", m)
		}
	}
}

// Silence when there is nothing to say: a machine with no session gets the new
// core on its next launch, full stop. Noise here would train people to skip the
// message that matters.
func TestNothingIsSaidWhenNoSessionRuns(t *testing.T) {
	if m := datapathLagMessage(0); m != "" {
		t.Errorf("datapathLagMessage(0) = %q, want silence", m)
	}
	if m := downStrandsSessions(0); m != "" {
		t.Errorf("downStrandsSessions(0) = %q, want silence", m)
	}
}

// The count is the whole point. "Close your sessions" is what was already being
// said, and it failed: the user believed they had, one was still open, and the
// daemon never got its quiet window. The number is what makes the advice
// actionable rather than reassuring.
func TestTheAdviceCarriesTheSessionCount(t *testing.T) {
	for _, n := range []string{"1", "2", "7"} {
		if m := datapathLagMessage(atoiTest(n)); !strings.Contains(m, n) {
			t.Errorf("datapathLagMessage(%s) = %q, want the count in it", n, m)
		}
		if m := downStrandsSessions(atoiTest(n)); !strings.Contains(m, n) {
			t.Errorf("downStrandsSessions(%s) = %q, want the count in it", n, m)
		}
	}
}

// After an update, the message must say what to do (close them ALL) and why it
// works on its own — not merely that something is out of date.
func TestTheUpdateMessageSaysCloseThemAllAndThatItSelfResolves(t *testing.T) {
	m := datapathLagMessage(2)
	if !strings.Contains(m, "Close them ALL") {
		t.Errorf("message = %q, want it to say to close them ALL", m)
	}
	if !strings.Contains(m, "30s") {
		t.Errorf("message = %q, want the 30s window — it is why no command is needed", m)
	}
}

// `plug down` has to state the damage before doing it, and offer the cheaper
// route. It used to print "stopped the plug daemon" as if nothing else happened.
func TestDownWarnsAboutTheDamageAndOffersTheAlternative(t *testing.T) {
	m := downStrandsSessions(3)
	if !strings.Contains(m, "lose their datapath") {
		t.Errorf("warning = %q, want it to name what the sessions lose", m)
	}
	if !strings.Contains(m, "relaunched") {
		t.Errorf("warning = %q, want it to say they must be relaunched", m)
	}
	if !strings.Contains(m, "just close them") {
		t.Errorf("warning = %q, want the cheaper alternative", m)
	}
}

func atoiTest(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
