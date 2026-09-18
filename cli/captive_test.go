package main

import "testing"

// The shape this exists for, and it is a real incident: hand-set public
// resolvers on a train's wifi. The portal answers DNS from the address it hands
// out over DHCP; queries sent to 1.1.1.1 go nowhere until you authenticate, and
// you cannot authenticate because the page never loads.
func TestHandSetResolversBehindAPortalAreRecognised(t *testing.T) {
	f := captiveFacts{
		configured: []string{"1.1.1.1", "1.0.0.1", "8.8.8.8"},
		dhcp:       []string{"192.168.10.1"},
		gateway:    "192.168.10.1",
		known:      true,
	}
	if got := captiveVerdict(f, false, true); got != "Cloudflare" {
		t.Fatalf("got %q, want the first public resolver's name", got)
	}
}

// Every condition is load-bearing. Drop any one of them and the verdict must go
// quiet: a wrong captive-portal warning sends somebody to rewrite their DNS
// settings for a problem they do not have.
func TestEveryConditionIsRequired(t *testing.T) {
	base := captiveFacts{
		configured: []string{"1.1.1.1"},
		dhcp:       []string{"192.168.10.1"},
		gateway:    "192.168.10.1",
		known:      true,
	}
	cases := []struct {
		why           string
		f             captiveFacts
		answers, gwUp bool
	}{
		{"the resolvers answer, so nothing is being held", base, true, true},
		{"the gateway is silent too: no network at all, not a portal", base, false, false},
		{"this OS has no collector yet", captiveFacts{configured: base.configured, dhcp: base.dhcp, gateway: base.gateway}, false, true},
		{"the resolvers came from DHCP: a broken network, not an override",
			captiveFacts{configured: []string{"192.168.10.1"}, dhcp: []string{"192.168.10.1"}, gateway: "192.168.10.1", known: true}, false, true},
		{"one resolver is not public, so this is not the shape",
			captiveFacts{configured: []string{"1.1.1.1", "192.168.10.1"}, dhcp: []string{"192.168.10.1"}, gateway: "192.168.10.1", known: true}, false, true},
		{"the network itself offers public resolvers: nothing was overridden",
			captiveFacts{configured: []string{"1.1.1.1"}, dhcp: []string{"8.8.8.8"}, gateway: "192.168.10.1", known: true}, false, true},
		{"nothing known about DHCP", captiveFacts{configured: base.configured, gateway: base.gateway, known: true}, false, true},
		{"no resolvers at all", captiveFacts{dhcp: base.dhcp, gateway: base.gateway, known: true}, false, true},
	}
	for _, c := range cases {
		if got := captiveVerdict(c.f, c.answers, c.gwUp); got != "" {
			t.Errorf("fired anyway (%s): %q", c.why, got)
		}
	}
}

// The configured servers come without a port and the captured ones come with
// one. Both have to be recognised, or the verdict would depend on whether a
// session happens to be running.
func TestAPublicResolverIsRecognisedWithOrWithoutItsPort(t *testing.T) {
	for _, in := range []string{"1.1.1.1", "1.1.1.1:53", " 1.1.1.1 "} {
		if publicResolverName(in) != "Cloudflare" {
			t.Errorf("%q was not recognised", in)
		}
	}
	for _, in := range []string{"192.168.1.1", "10.0.0.53:53", "", "not-an-address"} {
		if n := publicResolverName(in); n != "" {
			t.Errorf("%q was taken for %s", in, n)
		}
	}
}
