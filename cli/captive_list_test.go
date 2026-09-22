package main

import (
	"reflect"
	"testing"
)

// The registry stores a resolver list as one string, and Windows itself is not
// consistent about the separator: DhcpNameServer uses spaces, NameServer uses
// commas when typed through the UI, and both have been seen trailing.
func TestSplitResolverListReadsEveryShapeWindowsWrites(t *testing.T) {
	for raw, want := range map[string][]string{
		"192.168.1.254 192.168.1.253": {"192.168.1.254", "192.168.1.253"},
		"1.1.1.1,8.8.8.8":             {"1.1.1.1", "8.8.8.8"},
		"1.1.1.1, 8.8.8.8,":           {"1.1.1.1", "8.8.8.8"},
		"":                            nil,
		"  ":                          nil,
	} {
		if got := splitResolverList(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("splitResolverList(%q) = %v, want %v", raw, got, want)
		}
	}
}

// And through the shared rule, the Windows facts as the collector would hand
// them: hand-typed public resolvers over a network that offered its own is the
// captive-portal shape; DHCP resolvers that fail are a broken network, not this.
func TestWindowsFactsReachTheSharedVerdict(t *testing.T) {
	f := captiveFacts{known: true, gateway: "192.168.1.254",
		configured: splitResolverList("1.1.1.1,8.8.8.8"), dhcp: splitResolverList("192.168.1.254")}
	if got := captiveVerdict(f, false, true); got == "" {
		t.Fatal("hand-typed public resolvers that do not answer, over a network that offered its own, is the portal shape")
	}
	f.configured = splitResolverList("192.168.1.254")
	if got := captiveVerdict(f, false, true); got != "" {
		t.Fatalf("DHCP resolvers that fail are a broken network, not a portal: %q", got)
	}
}
