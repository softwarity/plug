package main

import (
	"os"
	"strings"
	"testing"
)

// The rule "one cluster, one account" is only worth as much as its placement. It
// has to be asked BEFORE the registration, because registering is what makes a
// process a member: ask afterwards and the intruder is already in the set it is
// being compared against. And it has to be asked in the SAME step, under the
// registry lock, or two accounts launched together both find the cluster free.
// RegisterClientExclusive is that step; the launchers must go through it and
// nothing else. The two files are per-OS, so a change made on one is easy to
// forget on the other, and a unit test on the registry would keep passing with
// the launchers calling RegisterClient directly.
func TestTheLauncherAsksBeforeItRegisters(t *testing.T) {
	for _, path := range []string{"socks_run_darwin.go", "socks_run_windows.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		s := string(src)
		if !strings.Contains(s, "tun.RegisterClientExclusive(") {
			t.Fatalf("%s no longer registers through RegisterClientExclusive: if that moved, move this test with it", path)
		}
		if strings.Contains(s, "tun.RegisterClient(") || strings.Contains(s, "tun.ClusterHeldByOther(") {
			t.Fatalf("%s asks who holds the cluster and registers as two steps. Between them a second "+
				"account launched at the same moment finds the cluster free too, and both are in", path)
		}
	}
}
