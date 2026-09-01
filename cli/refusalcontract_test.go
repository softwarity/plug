package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// heldPort reads a message the AGENT composes, in the other Go module, to decide
// whether the session holding a name is one of yours and therefore whether it is
// safe to OFFER to stop it. That is a contract between two modules carried
// entirely by the shape of a sentence, and until the agent side became a single
// constant it was six copies of a format string, five of them out of sight of
// the sixth. One losing its "(%s)" would have broken this offer on one backend
// and left the other five working.
//
// So the contract is checked: take the agent's own constant, render it the way
// the agent does, and run it through the parser that has to read it.
func TestTheCLIStillReadsTheAgentsRefusal(t *testing.T) {
	src, err := os.ReadFile("../agent/main.go")
	if err != nil {
		t.Fatalf("reading the agent: %v", err)
	}
	m := regexp.MustCompile(`const nameHeldRefusal = "(.*)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("agent/main.go no longer declares nameHeldRefusal: if the refusal moved or went back to " +
			"being written inline, move this test with it, because heldPort still has to read it")
	}
	format := strings.ReplaceAll(string(m[1]), `\"`, `"`)

	// Both shapes the agent produces: with an origin and without, since it stays
	// silent about the origin when the lease predates that field.
	for _, held := range []string{"agent port 2200, from 10.1.2.3", "agent port 2200"} {
		msg := fmt.Sprintf(format, "api", held)
		// Checked FIRST, because Go is helpful in exactly the wrong way here: a
		// format that no longer takes the holder still renders it, appended as
		// "%!(EXTRA string=agent port 2200)", and heldPort finds its marker in
		// THAT. The first version of this test passed against a refusal that had
		// lost its second verb, which is the failure it was written to catch.
		if strings.Contains(msg, "%!") {
			t.Fatalf("the agent's refusal no longer takes the holder as an argument, so the CLI has\n"+
				"nothing to match a local record against and will never offer to stop a session you own.\n"+
				"rendered: %s", msg)
		}
		if got := heldPort(msg); got != "2200" {
			t.Errorf("the CLI reads %q out of the agent's refusal, want the holder's port 2200.\n"+
				"refusal: %s", got, msg)
		}
		if !holderIsOurs(&servedRecord{port: "2200"}, msg) {
			t.Errorf("a record naming the very port the agent refused us for was not recognised as ours,\n"+
				"so plug would not offer to stop a session the user owns. refusal: %s", msg)
		}
		if holderIsOurs(&servedRecord{port: "2201"}, msg) {
			t.Errorf("a record naming a DIFFERENT port was taken for the holder, which is how an innocent\n"+
				"process ends up behind a \"stop it?\" prompt. refusal: %s", msg)
		}
	}
}

// An agent too old to name a port must simply get no offer, never a wrong one.
func TestARefusalWithoutAPortOffersNothing(t *testing.T) {
	if got := heldPort("error: \"api\" is already exposed by another live session"); got != "" {
		t.Errorf("heldPort invented %q out of a refusal that names no port", got)
	}
	if holderIsOurs(&servedRecord{port: "2200"}, "error: no port here") {
		t.Error("a refusal naming no port was matched against a local record")
	}
}
