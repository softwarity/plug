package tun

import (
	"reflect"
	"testing"
)

// A corporate VPN pushes a resolver for ITS domain and nothing else - macOS calls
// it SupplementalMatchDomains, Windows the NRPT. The machine's ordinary servers
// keep answering for the rest of the world. plug took the ordinary servers alone
// and routed everything through them, so the VPN's own names were the first
// thing a session broke: a resolver that has never heard of odb.fint.vn says so,
// confidently, and that answer won the race.
//
// This is the exact layout read off a laptop on OpenVPN Connect.
func TestAScopedResolverAnswersForItsDomainAndNothingElse(t *testing.T) {
	box := []string{"192.168.1.254:53"}
	vpn := []scopedUpstream{{domain: "fint.vn", addrs: []string{"172.16.1.254:53", "172.16.1.253:53"}}}

	for name, want := range map[string][]string{
		"odb.fint.vn":          vpn[0].addrs,
		"gitlab.fint.vn.":      vpn[0].addrs, // trailing dot, as on the wire
		"ODB.FINT.VN":          vpn[0].addrs, // case is not a different name
		"fint.vn":              vpn[0].addrs, // the apex itself
		"github.com":           box,
		"fint.vn.example.com":  box, // a name that merely CONTAINS the domain
		"notfint.vn":           box, // a suffix that is not on a label boundary
		"registry-1.docker.io": box,
	} {
		if got := pickScoped(name, vpn, box); !reflect.DeepEqual(got, want) {
			t.Errorf("%s -> %v, want %v", name, got, want)
		}
	}
}

// Scopes nest, and the more specific one is the one that knows the name.
func TestTheLongestMatchingScopeWins(t *testing.T) {
	def := []string{"1.1.1.1:53"}
	scopes := []scopedUpstream{
		{domain: "example", addrs: []string{"10.0.0.1:53"}},
		{domain: "corp.example", addrs: []string{"10.0.0.2:53"}},
	}
	if got := pickScoped("db.corp.example", scopes, def); got[0] != "10.0.0.2:53" {
		t.Fatalf("db.corp.example went to %v, want the corp.example resolver", got)
	}
	if got := pickScoped("www.example", scopes, def); got[0] != "10.0.0.1:53" {
		t.Fatalf("www.example went to %v, want the example resolver", got)
	}
}

// What the collector hands over is not tidy, and setScoped has to make it so:
// mixed case, dots on either end, a scope with no server at all.
func TestScopesAreNormalisedOnTheWayIn(t *testing.T) {
	u := newUpstream([]string{"192.168.1.254"})
	u.setScoped([]scopedUpstream{
		{domain: " .Fint.VN. ", addrs: []string{"172.16.1.254"}},
		{domain: "empty.example", addrs: nil}, // nothing answers: must not match
	})
	if got := u.serversFor("odb.fint.vn"); len(got) != 1 || got[0] != "172.16.1.254:53" {
		t.Fatalf("odb.fint.vn -> %v", got)
	}
	if got := u.serversFor("x.empty.example"); got[0] != "192.168.1.254:53" {
		t.Fatalf("a scope with no server captured the name: %v", got)
	}
}

// No scopes at all is every session before this change, and must behave
// exactly as it did.
func TestWithoutScopesEverythingGoesToTheDefaultServers(t *testing.T) {
	u := newUpstream([]string{"192.168.1.254"})
	if got := u.serversFor("anything.at.all"); got[0] != "192.168.1.254:53" {
		t.Fatalf("got %v", got)
	}
}
