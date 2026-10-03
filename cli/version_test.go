package main

// Every predicate that reads a version string, measured on one table. The
// grammar is documented once in version.go; these are the shapes it has to get
// right, gathered from the three tests that used to exercise three parsers.

import (
	"strings"
	"testing"
)

func TestParseVersionReadsEveryShapePlugStamps(t *testing.T) {
	for _, c := range []struct {
		in                  string
		ok                  bool
		major, minor, patch int
		flavour             string
		release             bool
		why                 string
	}{
		{"2.0.0", true, 2, 0, 0, "", true, "a bare release, as stamped since 2.4.1"},
		{"2.10.3", true, 2, 10, 3, "", true, "two-digit minor"},
		{"10.2.1", true, 10, 2, 1, "", true, "two-digit major"},
		{"2.2.0+3503368", true, 2, 2, 0, "", true, "a release from before 2.4.1 carries its revision"},
		{"v2.5.4", true, 2, 5, 4, "", true, "a registry tag may carry the v"},
		{"2.4", true, 2, 4, 0, "", false, "a stream tag stops at x.y: a version, not a release"},
		{"2.12.0-hosted", true, 2, 12, 0, "hosted", false, "a flavoured release: published, but another lineage"},
		{"2.12.0-hosted+9f2a1c", true, 2, 12, 0, "hosted", false, "flavour then revision"},
		{"2.12.0+9f2a1c-hosted", true, 2, 12, 0, "hosted", false, "revision then flavour, the way the image build stamps it"},
		{"dev", false, 0, 0, 0, "", false, "a dev build"},
		{"dev+abc1234", false, 0, 0, 0, "", false, "a branch build"},
		{"dev+e9ad3d1-hosted", false, 0, 0, 0, "", false, "a hosted branch build"},
		{"", false, 0, 0, 0, "", false, "an agent that names no version"},
		{"2", false, 0, 0, 0, "", false, "a major alone is a stream tag, not a version"},
		{".5", false, 0, 0, 0, "", false, ""},
		{"garbage", false, 0, 0, 0, "", false, ""},
		{"latest", false, 0, 0, 0, "", false, "a moving tag"},
		{"not-a-version", false, 0, 0, 0, "", false, "a dash does not make a flavour of just anything"},
		{"2.12.0-hosted-something", false, 0, 0, 0, "", false, "only one flavour word, like hosted()"},
		{"2.12.0-", false, 0, 0, 0, "", false, "an empty flavour"},
		{"2.+3.0", false, 0, 0, 0, "", false, "a sign is not a digit"},
		{"2.3.0.1", false, 0, 0, 0, "", false, "four parts"},
	} {
		v, ok := parseVersion(c.in)
		if ok != c.ok {
			t.Errorf("parseVersion(%q) ok=%v, want %v (%s)", c.in, ok, c.ok, c.why)
			continue
		}
		if !ok {
			continue
		}
		if v.major != c.major || v.minor != c.minor || v.patch != c.patch || v.flavour != c.flavour {
			t.Errorf("parseVersion(%q) = %d.%d.%d flavour %q, want %d.%d.%d flavour %q (%s)",
				c.in, v.major, v.minor, v.patch, v.flavour, c.major, c.minor, c.patch, c.flavour, c.why)
		}
		if v.release() != c.release {
			t.Errorf("parseVersion(%q).release() = %v, want %v (%s)", c.in, v.release(), c.release, c.why)
		}
	}
}

// Two releases compare numerically, and a comparison with anything that is not
// a release is void: never act on one.
func TestReleasesCompareNumerically(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"2.2.0", "2.3.0", true},
		{"2.3.0", "2.2.0", false},
		{"2.2.0", "2.2.0", false},
		{"2.9.9", "2.10.0", true}, // numeric, not lexical
		{"2.2.0", "3.0.0", true},
		{"dev+abc", "2.3.0", false}, // a dev side means the comparison is void
		{"2.2.0", "dev+abc", false},
		{"2.2", "2.3.0", false}, // a stream is not a release
		// Published images stamp VERSION+GIT_REV: the metadata must not turn a
		// release into a "dev build" (bench-caught against the real 2.2.0 image).
		{"2.2.0+3503368", "2.3.0+abc1234", true},
		{"2.3.0+abc1234", "2.2.0+3503368", false},
		// The two lineages must not be comparable either way.
		{"2.11.0", "2.12.0-hosted", false},
		{"2.12.0-hosted", "2.13.0", false},
	} {
		if got := releaseLess(c.a, c.b); got != c.less {
			t.Errorf("releaseLess(%q, %q) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	if isRelease("dev+abc") || isRelease("2.2") || isRelease("2.12.0-hosted") || !isRelease("2.2.0") || !isRelease("2.2.0+3503368") {
		t.Error("isRelease: dev, short or flavoured accepted, or a release refused")
	}
}

// A version this build cannot parse must never be read as OLD. Being wrong that
// way refuses a feature the agent has; being wrong the other way merely skips a
// guard aimed at agents that predate it.
func TestAnUnreadableVersionIsAssumedRecent(t *testing.T) {
	for _, v := range []string{"dev", "dev+9f2a1c", "", "unknown", "nightly", "2", "garbage", ".5"} {
		if versionBefore(v, 2, 12) {
			t.Errorf("versionBefore(%q) says old: a dev build would be refused features it has", v)
		}
		if versionBefore(v, 2, 0) {
			t.Errorf("versionBefore(%q, 2, 0) says old: -s would be refused to a dev build", v)
		}
	}
}

// The guards read major.minor in ANY lineage: an old hosted core drops a
// profile key exactly as an old standalone one does, and a dev build of either
// is recent.
func TestVersionBeforeReadsMajorAndMinor(t *testing.T) {
	for _, c := range []struct {
		v        string
		maj, min int
		want     bool
	}{
		{"1.9.3", 2, 0, true}, // released, predates -s
		{"2.0.0", 2, 0, false},
		{"10.2.1", 2, 0, false},
		{"2.1.0", 2, 2, true},
		{"2.2.0", 2, 2, false},
		{"2.11.1", 2, 12, true},
		{"2.12.0", 2, 12, false},
		{"2.12.0-hosted", 2, 12, false},
		{"2.11.0-hosted", 2, 12, true},
		{"3.0.0", 2, 12, false},
		{"2.10.3+abc", 2, 20, true},
	} {
		if got := versionBefore(c.v, c.maj, c.min); got != c.want {
			t.Errorf("versionBefore(%q, %d, %d) = %v, want %v", c.v, c.maj, c.min, got, c.want)
		}
	}
}

// A flavoured release is a PUBLISHED build, not a branch one: reading it as a
// branch build turns a digest mismatch from "corruption or tampering, say so"
// into a silent re-download. And the two lineages must NOT be comparable:
// newestOf picks what a standalone agent should retarget to, and a hosted tag
// is not a newer version of a standalone one but a different product built
// from the same commit. Letting it win would point a cluster at an image whose
// client has no `update`.
func TestTheFlavouredLineageIsPublishedButNotARelease(t *testing.T) {
	for _, v := range []string{"2.12.0", "2.12.0-hosted"} {
		if _, published := parseVersion(v); !published {
			t.Errorf("parseVersion rejects %q, so ensureVersion treats a published build as a branch one "+
				"and re-fetches a mismatched digest without a word", v)
		}
		if got := shortVersion(v + "+9f2a1c"); got != v {
			t.Errorf("shortVersion(%q+rev) = %q, want %q", v, got, v)
		}
	}
	if got := newestOf([]string{"2.11.0", "2.12.0-hosted"}); got != "2.11.0" {
		t.Errorf("newestOf picked %q: a hosted tag is not a newer standalone release", got)
	}
	if releaseNewerThan("2.13.0-hosted", "2.12.0") {
		t.Error("a hosted tag was read as a newer release of the standalone lineage")
	}
	if !releaseNewerThan("2.13.0", "2.12.0+abc") || releaseNewerThan("2.12.0", "2.12.0") || releaseNewerThan("2.13.0", "dev+abc") {
		t.Error("releaseNewerThan: the plain cases moved")
	}
}

// shortVersion is what every message shows. A release tag already designates
// one commit, so its build metadata is noise; a branch build without its
// revision would name every build of every branch; and a flavour must not be
// turned into something a reader would mistake for a different product.
func TestShortVersion(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2.4.0+983761c", "2.4.0"},
		{"2.3.0+bb03611", "2.3.0"},
		{"2.0.0+20015cd", "2.0.0"},
		{"2.4", "2.4"},
		{"2.4+abc1234", "2.4"},
		{"2.4.1", "2.4.1"},             // already bare (2.4.1 and later)
		{"dev+1ca6a07", "dev+1ca6a07"}, // the revision IS the identity here
		{"dev", "dev"},
		{"", ""},
		{"2.12.0-hosted", "2.12.0-hosted"},
	} {
		if got := shortVersion(c.in); got != c.want {
			t.Errorf("shortVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := shortVersion("2.12.0-hosted+9f2a1c"); !strings.HasSuffix(got, "-hosted") {
		t.Errorf("shortVersion dropped the flavour: %q", got)
	}
}
