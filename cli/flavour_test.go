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
		withVersion(t, c.v, func() {
			if got := hosted(); got != c.want {
				t.Errorf("version %q -> hosted()=%v, want %v", c.v, got, c.want)
			}
		})
	}
}

func TestEachFlavourCarriesItsOwnVerbs(t *testing.T) {
	common := []string{"init", "ls", "rm", "rn", "config", "test", "doctor",
		"prune", "down", "uninstall", "version", "about", "selftest",
		"install-service", "remove-service"}

	withVersion(t, "2.12.0", func() {
		for _, v := range append([]string{"update", "versions"}, common...) {
			if _, ok := verbAvailable(v); !ok {
				t.Errorf("standalone build refuses %q, which belongs to it", v)
			}
		}
		for _, v := range []string{"keygen", "pubkey"} {
			if _, ok := verbAvailable(v); ok {
				t.Errorf("standalone build carries %q, which needs a host that checks keys", v)
			}
		}
	})

	withVersion(t, "2.12.0-hosted", func() {
		for _, v := range append([]string{"keygen", "pubkey"}, common...) {
			if _, ok := verbAvailable(v); !ok {
				t.Errorf("hosted build refuses %q, which belongs to it", v)
			}
		}
		for _, v := range []string{"update", "versions"} {
			if _, ok := verbAvailable(v); ok {
				t.Errorf("hosted build carries %q: the gateway decides its client's version", v)
			}
		}
	})
}

// A help that advertises a command which then refuses is worse than no help.
// This is the case a reader hits first, so it is the one that must not slip.
func TestTheHelpNeverAdvertisesAVerbThisBuildRefuses(t *testing.T) {
	for _, v := range []string{"2.12.0", "2.12.0-hosted"} {
		withVersion(t, v, func() {
			help := usage()
			for _, verb := range []string{"update", "versions", "keygen", "pubkey"} {
				listed := strings.Contains(help, "plug "+verb+" ")
				_, available := verbAvailable(verb)
				if listed != available {
					t.Errorf("version %q: help lists %q=%v but the build answers it=%v",
						v, verb, listed, available)
				}
			}
		})
	}
}

// Every verb the help DOES list has to work, in both flavours. Trimming a
// section by hand is how a common verb disappears from one of the two.
func TestTheHelpKeepsEveryCommonVerb(t *testing.T) {
	for _, v := range []string{"2.12.0", "2.12.0-hosted"} {
		withVersion(t, v, func() {
			help := usage()
			for _, verb := range []string{"ls", "test", "doctor", "rn", "rm", "prune", "uninstall", "about"} {
				if !strings.Contains(help, "plug "+verb+" ") {
					t.Errorf("version %q: the help lost the common verb %q", v, verb)
				}
			}
		})
	}
}

// The refusal has to say WHY, and must not talk about build flags: the person
// reading it did not choose this build, they installed from a cluster.
func TestTheRefusalNamesTheReasonAndNotTheMechanism(t *testing.T) {
	withVersion(t, "2.12.0-hosted", func() {
		why, ok := verbAvailable("update")
		if ok {
			t.Fatal("hosted must refuse update")
		}
		if why == "" {
			t.Error("the refusal carries no reason")
		}
		for _, forbidden := range []string{"ldflags", "build tag", "flavour", "compile"} {
			if strings.Contains(strings.ToLower(why), forbidden) {
				t.Errorf("the reason talks about the mechanism: %q", why)
			}
		}
	})
}

// Both spellings of the same command must agree. `plug update -p X` is caught by
// the top-level dispatch, `plug -p X update` only reaches launcherRun, and a
// guard in one place leaves the other running the verb this build should not
// carry.
func TestBothSpellingsOfASubcommandAreGuarded(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// The re-route block must consult verbAvailable before dispatching.
	route := src[strings.Index(src, "func launcherRun("):]
	route = route[:strings.Index(route, "switch sub {")]
	if !strings.Contains(route, "verbAvailable(cmdArgs[0])") {
		t.Error("launcherRun re-routes `plug -p X update` without checking the flavour")
	}
}
