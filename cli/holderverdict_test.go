package main

import (
	"strings"
	"testing"
)

const refusalFromHere = `"api" is already exposed by another live session (agent port 45000, from 10.1.2.3) - one -s per name at a time`

// The refusal carries the lease's origin when the agent wrote one, and nothing
// when it is older than that field. Both shapes must parse, and the port must
// never be mistaken for the address.
func TestHeldFromReadsTheOriginAndOnlyTheOrigin(t *testing.T) {
	for msg, want := range map[string]string{
		refusalFromHere: "10.1.2.3",
		`"api" is already exposed by another live session (agent port 45000) - one -s per name at a time`: "",
		`held (agent port 1, from 2001:db8::7)`:                                                           "2001:db8::7",
	} {
		if got := heldFrom(msg); got != want {
			t.Errorf("heldFrom(%q) = %q, want %q", msg, got, want)
		}
	}
	if heldPort(refusalFromHere) != "45000" {
		t.Fatalf("the port parser broke on a message with an origin: %q", heldPort(refusalFromHere))
	}
}

// Three verdicts, and the middle one is the fix. With no local record and a
// lease from THIS machine's address, the holder is a session of yours still
// closing, not a colleague on another machine - which is what it used to say.
func TestHolderVerdictDistinguishesAClosingSessionFromAColleague(t *testing.T) {
	local := &servedRecord{pid: 4242, port: "45000", cmd: "npm run dev"}

	got := holderVerdict("api", refusalFromHere, local, "10.1.2.3")
	if !strings.Contains(got, "held on this machine") || !strings.Contains(got, "kill 4242") {
		t.Fatalf("a live local record must name the process and the kill:\n%s", got)
	}

	got = holderVerdict("api", refusalFromHere, nil, "10.1.2.3")
	if !strings.Contains(got, "still closing") || strings.Contains(got, "another machine") {
		t.Fatalf("a lease from this machine with no local record is a closing session, not a colleague:\n%s", got)
	}

	got = holderVerdict("api", refusalFromHere, nil, "10.9.9.9")
	if !strings.Contains(got, "another machine") || strings.Contains(got, "still closing") {
		t.Fatalf("a lease from elsewhere is somebody else:\n%s", got)
	}

	// An agent that wrote no origin, or a transport with no address, must fall
	// back to the honest "elsewhere" rather than guess "yours".
	for _, c := range []struct{ msg, addr string }{
		{refusalFromHere, ""},
		{`held (agent port 45000)`, "10.1.2.3"},
		{`held (agent port 45000)`, ""}, // both empty: "" == "" must not read as "from here"
	} {
		if got := holderVerdict("api", c.msg, nil, c.addr); strings.Contains(got, "still closing") {
			t.Fatalf("no evidence of origin must not claim a closing session (msg=%q addr=%q):\n%s", c.msg, c.addr, got)
		}
	}
}
