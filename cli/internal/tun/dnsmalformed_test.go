package tun

import (
	"strings"
	"testing"
)

// answerDNS reads packets off the wire. On macOS the daemon repoints the whole
// machine's resolver at it, so anything on the box can send it anything, and a
// panic here takes the datapath down for every session.
//
// The code IS defensive: a length guard, a bound on the walk, a refusal to read
// past the buffer. What was missing is anything that says so. The one test
// parsed a single well-formed query, so every guard could be removed and it
// would still pass.
func TestAMalformedQueryNeverTakesTheResolverDown(t *testing.T) {
	full := query("api.internal", 1)

	cases := map[string][]byte{
		"nothing at all":            {},
		"one byte":                  {0x00},
		"a header and nothing else": full[:12],
		"a header cut in half":      full[:6],
		// A label claiming more bytes than the packet holds. The walk must stop,
		// not read into whatever follows in memory.
		"a label longer than the packet": append(append([]byte{}, full[:12]...), 0x40, 'a', 'b'),
		// 0xC0 starts a compression pointer, which this parser does not implement.
		// Read as a length it means 192 bytes, which the bound has to catch.
		"a compression pointer":           append(append([]byte{}, full[:12]...), 0xC0, 0x0C),
		"a pointer to itself":             append(append([]byte{}, full[:12]...), 0xC0, 0x0D, 0x00, 0x01, 0x00, 0x01),
		"a label of 255":                  append(append(append([]byte{}, full[:12]...), 0xFF), make([]byte, 20)...),
		"no terminating zero":             append(append([]byte{}, full[:12]...), 0x03, 'a', 'p', 'i'),
		"a truncated question":            full[:len(full)-2],
		"every byte set":                  bytesRepeat(0xFF, 64),
		"a header of zeroes":              make([]byte, 12),
		"a plausible header, no question": append(append([]byte{}, full[:2]...), make([]byte, 10)...),
	}

	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			// A panic here is the failure. The reply may be nil, may carry an
			// rcode: both are answers. Crashing is not.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("a malformed query took the resolver down, and on macOS it is the whole "+
						"machine's resolver: %v", r)
				}
			}()
			reply := answerDNS(q, newFaketab(fakeBase), nil, func(string) bool { return true })
			// Whatever comes back must at least be a DNS packet, or the client
			// gets bytes it cannot parse instead of a refusal it can act on.
			if len(reply) != 0 && len(reply) < 12 {
				t.Errorf("answered %d bytes, which is neither nothing nor a DNS header", len(reply))
			}
		})
	}
}

// And the happy path must still work, or the test above is satisfied by a parser
// that refuses everything. A single-label name is a cluster service, minted
// locally, so it needs no upstream.
func TestAWellFormedQueryStillAnswers(t *testing.T) {
	tab := newFaketab(fakeBase)
	reply := answerDNS(query("api", 1), tab, nil, func(string) bool { return true })
	if len(reply) < 12 {
		t.Fatalf("a valid query for a name the cluster knows got %d bytes back", len(reply))
	}
	if name, _ := parseName(reply, 12); !strings.EqualFold(name, "api") {
		t.Errorf("the reply echoes %q rather than the question asked", name)
	}
}

// A name plug does not own is relayed upstream, and a caller wired without one
// must be told so rather than taken down. This function answers the whole
// machine's resolver on macOS: a nil dereference here is every lookup on the box
// failing, not just plug's.
func TestNoUpstreamIsAnAnswerNotACrash(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a query for a name plug does not own crashed the resolver when no upstream was "+
				"wired: %v", r)
		}
	}()
	reply := answerDNS(query("example.com", 1), newFaketab(fakeBase), nil, func(string) bool { return true })
	if len(reply) < 12 {
		t.Fatalf("got %d bytes back, want a DNS reply carrying a failure the client can act on", len(reply))
	}
	if rcode := reply[3] & 0x0f; rcode != 2 {
		t.Errorf("rcode %d, want 2 (SERVFAIL): there is no upstream to ask, and saying NOERROR with no "+
			"answer would have the client believe the name does not exist", rcode)
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
