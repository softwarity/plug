package main

// The verb surface per flavour. Two published images carry two clients, and the
// promise is narrow and checkable: a verb that does not apply is absent from the
// help and refuses on execution. Nothing here tests a preference; each case is a
// way the promise breaks silently.

import (
	"os"
	"strings"
	"testing"
)

// withVersion runs f with the build stamp the release would apply, restoring it
// after. The flavour is DERIVED from that string, so this is the only lever.
func withVersion(t *testing.T, v string, f func()) {
	t.Helper()
	old := version
	version = v
	defer func() { version = old }()
	f()
}

// The flavour is derived, never stamped separately: two constants could
// disagree, and the disagreement would show up as two clients sharing one cache
// entry under ~/.plug/versions.
func TestTheFlavourComesFromTheVersionString(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
	}{
		{"2.12.0-hosted", true},
		{"dev+e9ad3d1-hosted", true},
		{"2.12.0", false},
		{"dev+e9ad3d1", false},
		{"dev", false},
		{"", false},
		{"2.12.0-hosted-something", false}, // only the exact suffix counts
		{"hosted", false},                  // not a suffix of the form -hosted
	} {
		if got := hostedVersion(c.v); got != c.want {
			t.Errorf("version %q -> hostedVersion()=%v, want %v", c.v, got, c.want)
		}
	}
}

// The commands are the same in both builds, so the help is too. A plug
// installed from a gateway also serves this machine's standalone profiles, and
// the reverse: a verb missing from one help is a verb missing for a cluster
// that has it.
func TestEveryBuildListsEveryCommand(t *testing.T) {
	for _, v := range []string{"2.12.0", "2.12.0-hosted"} {
		withVersion(t, v, func() {
			help := usage()
			for _, verb := range []string{"update", "versions", "keygen", "pubkey",
				"ls", "test", "doctor", "rn", "rm", "prune", "uninstall", "about"} {
				if !strings.Contains(help, "plug "+verb+" ") {
					t.Errorf("version %q: the help does not list %q", v, verb)
				}
			}
		})
	}
}

// Whether a cluster takes agent updates is the CLUSTER's answer, read from the
// version its agent announces, whatever the plug on this machine is. Would have
// caught: `plug -p <standalone> update` refused as "not part of this install"
// on a machine whose plug had last been installed from a gateway.
func TestTheClusterDecidesWhetherItTakesUpdates(t *testing.T) {
	for _, build := range []string{"2.24.0", "2.22.0-hosted"} {
		withVersion(t, build, func() {
			if !updatesOffered("2.24.0") {
				t.Errorf("a %s plug refuses updates to a standalone cluster", build)
			}
			if updatesOffered("2.22.0-hosted") {
				t.Errorf("a %s plug offers updates to a gateway's cluster", build)
			}
		})
	}
}

// The refusal has to say WHY, and must not talk about build flags: the person
// reading it did not choose this build, they installed from a cluster.
func TestTheGatewaysReasonNamesNoMechanism(t *testing.T) {
	for _, forbidden := range []string{"ldflags", "build tag", "flavour", "compile", "install"} {
		if strings.Contains(strings.ToLower(gatewayDecides), forbidden) {
			t.Errorf("the reason talks about the mechanism: %q", gatewayDecides)
		}
	}
}

// No command is refused for the flavour of the BUILD any more: both entry
// points (`plug update -p X` in main, `plug -p X update` in the launcher) let
// it through to the command, which asks the cluster.
func TestNoEntryPointRefusesByTheBuildsFlavour(t *testing.T) {
	for _, f := range []string{"main.go", "launcher.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "hosted()") {
			t.Errorf("%s decides something by the flavour of this build: the cluster's is the one that counts", f)
		}
	}
}
