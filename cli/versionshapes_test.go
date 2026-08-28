package main

// Every predicate that reads a version string, measured on the same table.
//
// The audit said three comparators disagreed on unparseable input. Measured
// here, they do not: they all treat what they cannot read as RECENT, which is
// the only safe direction (calling a new build old triggers a compatibility
// refusal against an agent that supports the feature perfectly well). What the
// audit could not see is the shape that did not exist yet: a FLAVOURED release,
// 2.12.0-hosted, which several of these read as a development build.

import (
	"strings"
	"testing"
)

// A version this build cannot parse must never be read as OLD. Being wrong that
// way refuses a feature the agent has; being wrong the other way merely skips a
// guard aimed at agents that predate it.
func TestAnUnreadableVersionIsAssumedRecent(t *testing.T) {
	for _, v := range []string{"dev", "dev+9f2a1c", "", "unknown", "nightly"} {
		if versionBefore(v, 2, 12) {
			t.Errorf("versionBefore(%q) says old: a dev build would be refused features it has", v)
		}
		if maj := coreMajor(v); maj >= 0 {
			t.Errorf("coreMajor(%q) = %d, want -1 so callers read it as recent", v, maj)
		}
		if min := coreMinor(v); min >= 0 {
			t.Errorf("coreMinor(%q) = %d, want -1", v, min)
		}
	}
}

// A released version is read as released by every predicate, flavour included.
// This is the shape the audit predates: `2.12.0-hosted` is a PUBLISHED release,
// not a branch build, and reading it as one turns a digest mismatch from
// "corruption or tampering, say so" into a silent re-download.
func TestAFlavouredReleaseIsStillARelease(t *testing.T) {
	for _, v := range []string{"2.12.0", "2.12.0-hosted"} {
		if !releaseVersionRe.MatchString(v) {
			t.Errorf("releaseVersionRe rejects %q, so ensureVersion treats a published build as a branch one "+
				"and re-fetches a mismatched digest without a word", v)
		}
		if got := shortVersion(v + "+9f2a1c"); got != v {
			t.Errorf("shortVersion(%q+rev) = %q, want %q", v, got, v)
		}
		if coreMajor(v) != 2 || coreMinor(v) != 12 {
			t.Errorf("%q parses as %d.%d, want 2.12", v, coreMajor(v), coreMinor(v))
		}
		if versionBefore(v, 2, 12) {
			t.Errorf("versionBefore(%q, 2, 12) says old", v)
		}
	}
}

// And the two lineages must NOT be comparable. `newestOf` picks what a
// standalone agent should retarget to; a hosted tag is not a newer version of a
// standalone one, it is a different product built from the same commit. Letting
// it win would point a cluster at an image whose client has no `update`.
func TestTheFlavouredLineageIsNotARelease(t *testing.T) {
	if got := newestOf([]string{"2.11.0", "2.12.0-hosted"}); got != "2.11.0" {
		t.Errorf("newestOf picked %q: a hosted tag is not a newer standalone release", got)
	}
	if releaseNewerThan("2.13.0-hosted", "2.12.0") {
		t.Error("a hosted tag was read as a newer release of the standalone lineage")
	}
}

// shortVersion is what every message shows. It must not turn a flavour into
// something a reader would mistake for a different product.
func TestTheFlavourSurvivesIntoWhatIsPrinted(t *testing.T) {
	if got := shortVersion("2.12.0-hosted+9f2a1c"); !strings.HasSuffix(got, "-hosted") {
		t.Errorf("shortVersion dropped the flavour: %q", got)
	}
}
