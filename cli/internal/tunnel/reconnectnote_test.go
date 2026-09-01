package tunnel

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The reconnect nobody asked for is the one that must speak. A laptop wakes, a
// VPN blinks, the keepalive notices and replaces the connection: the user sees
// "agent unreachable" when the re-dial fails, and used to see nothing at all
// when it worked, which reads like the outage never ended.
//
// The note used to hang off holding a stale client to close, and the keepalive
// path never holds one: dropDead closes and clears before re-dialling.
func TestAKeepaliveReconnectTellsTheUser(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, notes := connectedTransport(t, agent)
	defer tr.Close()

	// The ping fails, the agent stays reachable. That is the real shape of this:
	// a laptop wakes with a dead socket to a cluster that is perfectly fine, and
	// the keepalive replaces the connection. No flipping of state mid-test, which
	// only ever tested the test.
	tick := make(chan time.Time)
	go tr.keepaliveOn(tick, func(*ssh.Client) bool { return false })

	// Bounded by ticks rather than by a clock: every tick is received before the
	// previous one's body has returned, so the count is deterministic and a stuck
	// loop fails here instead of hanging.
	for i := 0; i < keepaliveMisses*4; i++ {
		tick <- time.Now()
		if strings.Contains(notes.all(), "re-established") {
			return
		}
	}
	t.Fatalf("the keepalive replaced the connection and said nothing; the user is left on the last "+
		"thing they were told, which was that the agent was unreachable. Notes were:\n%s", notes.all())
}

// And the FIRST connection must not announce itself as a re-connection, which is
// what a naive fix produces: nothing was established before it.
func TestTheFirstConnectionIsNotAnnouncedAsAReconnect(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, notes := connectedTransport(t, agent)
	defer tr.Close()

	if strings.Contains(notes.all(), "re-established") {
		t.Errorf("the first connection announced itself as re-established:\n%s", notes.all())
	}
}
