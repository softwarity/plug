package tun

import (
	"net"
	"testing"
)

// fakeResolver is a Dialer whose agent answers the resolve verb from a map.
type fakeResolver struct {
	names map[string]bool
	ok    bool
	calls int
}

func (f *fakeResolver) DialCluster(string) (net.Conn, error) { return nil, nil }
func (f *fakeResolver) ResolveInCluster(name string) (bool, bool) {
	f.calls++
	return f.names[name], f.ok
}

// plainDialer has no ResolveInCluster at all (an exotic Dialer impl).
type plainDialer struct{}

func (plainDialer) DialCluster(string) (net.Conn, error) { return nil, nil }

func TestNameCheckerVerdictsAndCache(t *testing.T) {
	fr := &fakeResolver{names: map[string]bool{"httpbin": true}, ok: true}
	check := newNameChecker(func() []Dialer { return []Dialer{fr} }, logfn(func(string, ...any) {}))

	if !check("httpbin") {
		t.Fatal("existing name must mint")
	}
	if check("absent") {
		t.Fatal("absent name must NXDOMAIN")
	}
	// Cached: two more lookups, zero extra round-trips.
	before := fr.calls
	check("httpbin")
	check("absent")
	if fr.calls != before {
		t.Fatalf("cache miss: %d extra agent calls", fr.calls-before)
	}
}

func TestNameCheckerFallsBackToMinting(t *testing.T) {
	log := logfn(func(string, ...any) {})
	// Old agent (ok=false): mint as before.
	old := &fakeResolver{names: map[string]bool{}, ok: false}
	if !newNameChecker(func() []Dialer { return []Dialer{old} }, log)("whatever") {
		t.Fatal("an old agent must fall back to minting")
	}
	// No transports at all: mint.
	if !newNameChecker(func() []Dialer { return nil }, log)("whatever") {
		t.Fatal("no transports must fall back to minting")
	}
	// A Dialer without the facet: mint.
	if !newNameChecker(func() []Dialer { return []Dialer{plainDialer{}} }, log)("whatever") {
		t.Fatal("a facet-less dialer must fall back to minting")
	}
}

func TestNameCheckerAnyClusterWins(t *testing.T) {
	a := &fakeResolver{names: map[string]bool{}, ok: true}
	b := &fakeResolver{names: map[string]bool{"svc": true}, ok: true}
	check := newNameChecker(func() []Dialer { return []Dialer{a, b} }, logfn(func(string, ...any) {}))
	if !check("svc") {
		t.Fatal("a name present in ANY connected cluster must mint")
	}
}

func TestAnswerDNSHonestNXDOMAIN(t *testing.T) {
	tab := newFaketab(fakeBase)
	deny := nameChecker(func(string) bool { return false })
	resp := answerDNS(query("ghost", 1), tab, upstreamResolver(nil), deny)
	if resp == nil {
		t.Fatal("no response")
	}
	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("want NXDOMAIN (3), got rcode %d", rcode)
	}
	if _, ok := tab.lookup(fakeBase | 1); ok {
		t.Fatal("a denied name must not be minted")
	}
}
