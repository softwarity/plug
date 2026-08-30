package main

// Three copies of relay(), and they must stay the same three.
//
// Two live in this module (internal/tun, internal/tunnel) and one in the agent,
// which is a separate Go module: sharing one would mean publishing a package for
// fifteen lines, and a published package is a versioned dependency between two
// halves of the same repo. Duplication is the deliberate choice.
//
// What is not deliberate is drift, and it had already happened. The netstack
// copy had lost the branch that closes a destination with no CloseWrite, so a
// direction could wait on an EOF that never came. It does not bite today,
// because both ends there implement CloseWrite - which is exactly why nobody
// noticed across three copies and however many readings.
//
// So the copies are compared. This is what check-common-block.sh does for the
// three e2e blocks, for the same reason: drift is silent, and nothing else
// fails when it happens.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var relayBody = regexp.MustCompile(`(?s)\nfunc relay\(a, b net\.Conn\) \{.*?\n\}`)

func relayFrom(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	m := relayBody.FindString(string(b))
	if m == "" {
		t.Fatalf("%s no longer defines relay(a, b net.Conn): if it moved, move this test with it", path)
	}
	return m
}

// The three must agree on WHAT THEY DO. They differ in how they wait for the two
// directions to finish - a channel here, a WaitGroup there - and that is not
// what drifted, so the comparison is on the behaviour that did: what happens to
// the destination when a copy ends.
func TestTheThreeRelayCopiesStillAgree(t *testing.T) {
	copies := map[string]string{
		"internal/tun/netstack.go":     relayFrom(t, "internal/tun/netstack.go"),
		"internal/tunnel/transport.go": relayFrom(t, "internal/tunnel/transport.go"),
		"../agent/main.go":             relayFrom(t, "../agent/main.go"),
	}
	for path, body := range copies {
		if !strings.Contains(body, "CloseWrite()") {
			t.Errorf("%s: relay no longer half-closes; the peer never learns the direction is done", path)
		}
		// The branch that drifted: a destination that cannot half-close must be
		// closed outright, or the other direction waits on an EOF nobody sends.
		if !strings.Contains(body, "} else {") || !strings.Contains(body, "dst.Close()") {
			t.Errorf("%s: relay does not close a destination without CloseWrite.\n"+
				"That is the branch the netstack copy had already lost. Whichever copy this is,\n"+
				"the other two have it and this one must too.", path)
		}
		if !strings.Contains(body, "a.Close()") || !strings.Contains(body, "b.Close()") {
			t.Errorf("%s: relay no longer closes both ends when it returns", path)
		}
	}
}
