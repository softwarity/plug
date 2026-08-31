package agent

import (
	"fmt"
	"strings"
	"testing"
)

// dispatch is where EVERY command arriving from the network lands: the SSH
// session hands it `SSH_ORIGINAL_COMMAND` and it decides what the agent does to
// the cluster. Nothing exercised it, because both of its exits leave the
// process. The gap was not theoretical: replacing nameRe with ^.*$ (accept any
// name at all, including one that k8s would reject or that carries a dot and
// aims at another namespace) left the entire agent suite green.
//
// These tests cover the REFUSALS only. A name that passes validation goes on to
// talk to Docker, Swarm or Kubernetes, which is the e2e families' job; what
// belongs here is the boundary itself, and the boundary is only visible when it
// says no.

// exitReply is what a refusal looks like once the exiting is taken away.
type exitReply struct{ said string }

// refusalFor runs dispatch with both exits swapped for a panic, and returns what
// the agent would have replied. An empty string means dispatch ACCEPTED the
// command and walked on into the orchestrator, which for these inputs is the
// failure being guarded against.
func refusalFor(t *testing.T, cmd ...string) string {
	t.Helper()
	realAnswer, realFatal := answer, fatal
	defer func() { answer, fatal = realAnswer, realFatal }()

	// A panic, not a plain record: the real answer() exits, so the lines after
	// each call assume they are unreachable. Letting a stub return would run the
	// serve path on input the agent had just rejected.
	stop := func(format string, a ...any) { panic(exitReply{fmt.Sprintf(format, a...)}) }
	answer, fatal = stop, stop

	var said string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			e, ok := r.(exitReply)
			if !ok {
				panic(r)
			}
			said = e.said
		}()
		dispatch(cmd)
	}()
	return said
}

func TestServeNameRefusesNamesTheClusterWouldNotAccept(t *testing.T) {
	// Every one of these is a name a backend would choke on, or that reaches
	// past the label it claims to be. k8s refuses a leading digit where docker
	// would take it, which is why the agent has to be stricter than either.
	for _, name := range []string{
		"Api",                   // uppercase
		"api_internal",          // underscore
		"1api",                  // leading digit, valid for docker, rejected by k8s
		"api-",                  // trailing dash
		"-api",                  // leading dash
		"api.internal",          // a dotted name, not a label
		"api internal",          // a space
		"",                      // nothing at all
		strings.Repeat("a", 64), // one over the RFC 1035 limit
	} {
		said := refusalFor(t, "serve-name", name, "80:2200", "takeover")
		if !strings.Contains(said, "not a valid DNS label") {
			t.Errorf("serve-name accepted %q, the cluster would be asked for a name it cannot hold (agent said %q)", name, said)
		}
	}
}

// The accept side cannot go through dispatch, which would then call the
// orchestrator, so it is asserted on the predicate dispatch uses. Without this,
// a nameRe of ^$ would pass every test above while refusing every real name.
func TestTheLabelRuleStillAcceptsRealNames(t *testing.T) {
	for _, name := range []string{"a", "api", "api-internal", "a1", strings.Repeat("a", 63)} {
		if !nameRe.MatchString(name) {
			t.Errorf("%q is a valid DNS label and the agent refuses it: no session could ever serve that name", name)
		}
	}
}

func TestServeNameRefusesPortsOutsideTheRange(t *testing.T) {
	for _, pair := range []string{
		"0:2200",     // port zero
		"65536:2200", // one over
		"-1:2200",    // negative
		"80:0",
		"80:65536",
		"abc:2200", // not a number
		"80:2200x", // trailing junk
		" 80:2200", // Atoi rejects the space, and it must
	} {
		said := refusalFor(t, "serve-name", "api", pair, "takeover")
		if !strings.Contains(said, "not a valid port") {
			t.Errorf("serve-name accepted the pair %q (agent said %q)", pair, said)
		}
	}
}

func TestServeNameRefusesAPairThatIsNotAPair(t *testing.T) {
	said := refusalFor(t, "serve-name", "api", "80", "takeover")
	if !strings.Contains(said, "is not <cluster-port>:<agent-port>") {
		t.Errorf("serve-name took a lone port as a pair (agent said %q)", said)
	}
}

// takeover is the word that says the caller KNOWS it may displace a running
// workload. A serve-name that reaches the cluster without it would park
// somebody's service on the strength of a truncated command line.
func TestServeNameRefusesWithoutTheTakeoverWord(t *testing.T) {
	for _, cmd := range [][]string{
		{"serve-name", "api", "80:2200"},
		{"serve-name", "api", "80:2200", "please"},
		{"serve-name", "api"},
		{"serve-name"},
		{"serve-name", "api", "80:2200", "takeover", "extra"},
	} {
		said := refusalFor(t, cmd...)
		if !strings.Contains(said, "usage: serve-name") {
			t.Errorf("%v was accepted, the agent said %q", cmd, said)
		}
	}
}

func TestUnserveNameChecksItsNameAndItsPort(t *testing.T) {
	for _, cmd := range [][]string{
		{"unserve-name"},                         // no name
		{"unserve-name", "Api"},                  // a name no session could hold
		{"unserve-name", "api", "2200", "extra"}, // one argument too many
	} {
		if said := refusalFor(t, cmd...); !strings.Contains(said, "usage: unserve-name") {
			t.Errorf("%v was accepted, the agent said %q", cmd, said)
		}
	}
	// The optional port says WHICH session is releasing the name. A bad one must
	// not fall through as "no port given", which would let a stale session tear
	// down its successor's forward.
	for _, port := range []string{"0", "65536", "-1", "abc"} {
		said := refusalFor(t, "unserve-name", "api", port)
		if !strings.Contains(said, "not a valid port") {
			t.Errorf("unserve-name took %q as a session port (agent said %q)", port, said)
		}
	}
}

func TestResolveNeedsAName(t *testing.T) {
	if said := refusalFor(t, "resolve"); !strings.Contains(said, "usage: resolve") {
		t.Errorf("resolve ran with no name, the agent said %q", said)
	}
}

func TestSelfUpdateChecksItsShape(t *testing.T) {
	for _, cmd := range [][]string{
		{"self-update", "apply"},                   // apply with no tag
		{"self-update", "apply", "2.0.0", "extra"}, // one too many
		{"self-update", "a", "b"},                  // two arguments, neither the apply form
	} {
		if said := refusalFor(t, cmd...); !strings.Contains(said, "usage: self-update") {
			t.Errorf("%v was accepted, the agent said %q", cmd, said)
		}
	}
}

// An empty command is what an SSH client that opened a session and said nothing
// produces. It must be told there is no shell here, not walk into a nil index.
func TestAnEmptyCommandIsTurnedAway(t *testing.T) {
	if said := refusalFor(t); !strings.Contains(said, "there is no shell") {
		t.Errorf("an empty command was not turned away, the agent said %q", said)
	}
}
