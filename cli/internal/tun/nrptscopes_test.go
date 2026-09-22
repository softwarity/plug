package tun

import (
	"reflect"
	"testing"
)

// One NRPT rule as the registry stores it: names with a leading dot, servers
// joined by ";". This is the shape a corporate VPN client writes, and the shape
// plug never read - a session sent every *.corp.example name to the ordinary
// resolver, which had never heard of them.
func TestAnNRPTRuleBecomesAScopedUpstream(t *testing.T) {
	got := nrptScopes([]string{".corp.example", ".Lab.Example."}, "10.0.0.1;10.0.0.2", "198.18.0.53")
	want := []scopedUpstream{
		{domain: "corp.example", addrs: []string{"10.0.0.1", "10.0.0.2"}},
		{domain: "lab.example", addrs: []string{"10.0.0.1", "10.0.0.2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// plug's own rule must not become a scope (it would route .plug to itself and
// loop), a rule with no usable server routes nowhere, and a server that is a
// fake-range address is another plug instance, not the machine's.
func TestPlugsOwnRuleAndEmptyRulesAreNotScopes(t *testing.T) {
	if got := nrptScopes([]string{"." + searchSuffix}, "198.18.0.53", "198.18.0.53"); got != nil {
		t.Fatalf("plug's own rule became a scope: %+v", got)
	}
	if got := nrptScopes([]string{".corp.example"}, "", "198.18.0.53"); got != nil {
		t.Fatalf("a rule with no server became a scope: %+v", got)
	}
	if got := nrptScopes([]string{".corp.example"}, "198.18.5.53;not-an-ip", "198.18.0.53"); got != nil {
		t.Fatalf("a rule with only unusable servers became a scope: %+v", got)
	}
}

// And end to end through the shared rule: the scope the collector produced is
// the one a name under that domain is routed to.
func TestAnNRPTScopeRoutesItsNames(t *testing.T) {
	u := newUpstream([]string{"192.168.1.254"})
	u.setScoped(nrptScopes([]string{".corp.example"}, "10.0.0.1", "198.18.0.53"))
	if got := u.serversFor("db.corp.example"); len(got) != 1 || got[0] != "10.0.0.1:53" {
		t.Fatalf("db.corp.example -> %v, want the NRPT rule's server", got)
	}
	if got := u.serversFor("github.com"); got[0] != "192.168.1.254:53" {
		t.Fatalf("github.com -> %v, want the ordinary server", got)
	}
}
