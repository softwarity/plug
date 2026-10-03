package agent

import (
	"strings"
	"testing"
)

// A derived name (a prefix in front of a workload's name) can outgrow the one
// label every backend accepts: nameRe lets a workload's name run to 63, and
// "plug-sp-" in front of one of 56 is already too long for Swarm. The fix must
// cut without renaming anything that fit before: the e2e suites and every
// session in flight know their objects by today's names.

func TestFitClusterNameLeavesAFittingNameAlone(t *testing.T) {
	for _, n := range []string{"web", "plug-sp-web", strings.Repeat("a", 63), "plug-mnt-" + strings.Repeat("w", 45) + "-1234abcd"} {
		if got := fitClusterName(n); got != n {
			t.Errorf("fitClusterName(%q) = %q: a name that fits must come back exactly as it was", n, got)
		}
	}
}

func TestFitClusterNameCutsAndSignsALongOne(t *testing.T) {
	long := "plug-sp-" + strings.Repeat("a", 63)
	got := fitClusterName(long)
	if len(got) != clusterNameMax {
		t.Fatalf("fitClusterName(%d chars) = %q, %d chars: wanted exactly %d", len(long), got, len(got), clusterNameMax)
	}
	if !nameRe.MatchString(got) {
		t.Fatalf("%q is not a DNS label", got)
	}
	if !strings.HasPrefix(got, "plug-sp-aaaa") {
		t.Fatalf("the prefix and the start of the name are what a human recognises, got %q", got)
	}
	if got != fitClusterName(long) {
		t.Fatal("the cut must be deterministic, or a session could not find its own object")
	}
	// Two names that agree on their first fifty characters are two names.
	other := fitClusterName(long[:len(long)-1] + "b")
	if other == got {
		t.Fatalf("%q and its one-character variant collapsed onto the same object %q", long, got)
	}
	// A cut that lands on a dash must not leave one before the digest: a label
	// ending or doubling a dash is the kind of thing only a cluster reports.
	dashy := "plug-sp-" + strings.Repeat("a-", 31) + "a"
	if got := fitClusterName(dashy); !nameRe.MatchString(got) || strings.Contains(got, "--") || len(got) > clusterNameMax {
		t.Fatalf("fitClusterName(%q) = %q", dashy, got)
	}
}

// The signpost's name is the one Swarm refuses first: "plug-sp-" plus a name of
// 56 is 64. 55 is the last to fit and must keep today's shape.
func TestSignpostNameFitsEveryBackend(t *testing.T) {
	n55 := strings.Repeat("s", 55)
	if got := signpostName(n55); got != "plug-sp-"+n55 {
		t.Fatalf("signpostName(55 chars) = %q: a name that fit before must not change", got)
	}
	for _, n := range []string{strings.Repeat("s", 56), strings.Repeat("s", 63)} {
		got := signpostName(n)
		if len(got) > clusterNameMax || !nameRe.MatchString(got) || !strings.HasPrefix(got, "plug-sp-") {
			t.Fatalf("signpostName(%d chars) = %q: not a label the cluster takes", len(n), got)
		}
	}
	if signpostName("") != "plug-sp-" {
		t.Fatalf("the bare prefix is what older signposts are recognised by, got %q", signpostName(""))
	}
}

// The sweep that removes a parked service's secret stash needs the served name,
// which the object's name no longer yields once cut: the label carries it, and
// the prefix-stripping stays for signposts an older agent created, which were
// never cut (a cut name of 63 is indistinguishable from a whole name of 55, so
// the label is the only record and must be written on every signpost).
func TestSignpostServedNameReadsTheLabelThenTheOldShape(t *testing.T) {
	long := strings.Repeat("s", 63)
	if got := signpostServedName(signpostName(long), map[string]string{signpostNameLabel: long}); got != long {
		t.Errorf("with the label, the served name is the label: got %q", got)
	}
	if got := signpostServedName("plug-sp-web", nil); got != "web" {
		t.Errorf("an older signpost's name, prefix off, is the served name: got %q", got)
	}
	if got := signpostServedName("web", nil); got != "" {
		t.Errorf("not a signpost's name at all: got %q", got)
	}
}
