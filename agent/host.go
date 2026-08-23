package agent

// The contract between the agent and whatever hosts it.
//
// Everything the agent does is compiled into the host: the SSH server, the
// verbs, the name provisioning. Three things are NOT, because they are the only
// ones a gateway can answer better than the agent can:
//
//   - the server's own identity (a vault, rather than a file in a container
//     that a recreated pod discards),
//   - which client keys are accepted, and under whose name (a database of
//     developers, rather than one key shared by everyone),
//   - what is being served right now (so a state page can say "serviceX by
//     Alice" instead of "serviceX by somebody").
//
// Standalone plug implements all three trivially, and that implementation is
// not a stub for tests: it IS the product's current behaviour, stated rather
// than implied. Nothing changes mode; a different Host is supplied, and the
// consequences follow.

import (
	"time"

	"golang.org/x/crypto/ssh"
)

// Host is what the agent asks of its surroundings. Small on purpose: every
// method here is something the agent genuinely cannot decide alone.
type Host interface {
	// HostKey is the server's identity, created on first use and kept
	// afterwards. Kept where the caller decides: a file in the container
	// standalone, a vault under Meerkat.
	//
	// Called once at startup. An error stops the agent, since SSH has no
	// meaning without one.
	HostKey() (ssh.Signer, error)

	// Verify answers whether a client key may open a tunnel, and under whose
	// name. An empty `who` means the key authenticates the SOFTWARE and not a
	// person, which is what a single shared key does and what standalone plug
	// says about itself.
	//
	// Called on every connection, so it must be cheap or cached.
	Verify(key ssh.PublicKey) (who string, ok bool)

	// Served reports a name now pointing at someone's machine, Unserved that it
	// no longer does.
	//
	// Both are best effort and MUST NOT fail a session: this is information,
	// never authorisation. A host that is slow, broken or absent must not stop
	// a developer from working.
	//
	// Unserved is not optional. Without it a state page shows names that are no
	// longer served, which is worse than showing nothing.
	Served(NameEvent)
	Unserved(name string)
}

// NameEvent is what the host learns when a name changes hands.
type NameEvent struct {
	Name   string    // the cluster name being served
	Who    string    // the client key's identity; empty when unauthenticated
	Ports  []string  // the cluster-side ports
	Parked bool      // whether a deployed workload was set aside for it
	Since  time.Time // when it started being served
}

// standaloneHost is plug without a gateway: one key from the image, an identity
// on disk, and nobody listening to the events.
//
// It is the honest description of what plug is today. The shared key is in every
// published binary, so it proves the caller HAS plug, not who they are, and Verify
// returns an empty name for exactly that reason.
type standaloneHost struct {
	authorized []ssh.PublicKey
	keyPath    string
}

func (h *standaloneHost) HostKey() (ssh.Signer, error) { return hostKeySigner(h.keyPath) }

func (h *standaloneHost) Verify(key ssh.PublicKey) (string, bool) {
	for _, a := range h.authorized {
		// Compare marshalled forms: PublicKey is an interface, and two values
		// describing the same key are not necessarily ==.
		if a.Type() == key.Type() && string(a.Marshal()) == string(key.Marshal()) {
			return "", true
		}
	}
	return "", false
}

// Nobody to tell. A standalone agent already refuses a second session on a name
// through its own lease, so the events add nothing here.
func (h *standaloneHost) Served(NameEvent) {}
func (h *standaloneHost) Unserved(string)  {}
