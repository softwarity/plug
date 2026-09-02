package main

import (
	"os"
	"strings"
	"testing"
)

// The rule "one cluster, one account" is only worth as much as its placement. It
// has to be asked BEFORE RegisterClient, because registering is what makes a
// process a member: ask afterwards and the intruder is already in the set it is
// being compared against. Nothing in the type system says so, and the two files
// that must obey it are per-OS, so a change made on one is easy to forget on the
// other. A unit test on ClusterHeldByOther proves the rule and would keep passing
// with the call deleted from both launchers.
func TestTheLauncherAsksBeforeItRegisters(t *testing.T) {
	for _, path := range []string{"socks_run_darwin.go", "socks_run_windows.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		s := string(src)
		guard := strings.Index(s, "tun.ClusterHeldByOther(")
		register := strings.Index(s, "tun.RegisterClient(")
		if register < 0 {
			t.Fatalf("%s no longer registers a client: if that moved, move this test with it", path)
		}
		if guard < 0 {
			t.Fatalf("%s registers a client without asking whether another account already holds the "+
				"cluster. That is how a second local account reached someone else's tunnel: it registered "+
				"first and was then compared against a set it had just joined", path)
		}
		if guard > register {
			t.Fatalf("%s asks who holds the cluster AFTER registering, which always answers 'you do'. "+
				"The question only means something before the marker exists", path)
		}
	}
}
