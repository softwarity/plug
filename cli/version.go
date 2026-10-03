package main

// One reading of a version string for the whole launcher.
//
// Three parsers used to live under different names (update.go, registry.go,
// launcher.go), each with its own tolerance for what surrounds the number, and
// they disagreed on a shape none had been written for: a flavoured release.
// This is the one grammar, and every predicate below derives from it:
//
//	[v] MAJOR . MINOR [. PATCH] [- FLAVOUR] [+ REV]
//
//   - MAJOR.MINOR[.PATCH]: decimal, compared numerically (2.9.9 < 2.10.0). A
//     registry tag may stop at x.y (a stream, "2.4"); a version an agent reports
//     always has its patch. Only an x.y.z of the standalone lineage is a RELEASE,
//     the shape the registry is asked about and two launchers are compared on.
//   - v: tolerated. Registry tags sometimes carry it; nothing plug stamps does.
//   - -FLAVOUR: a lineage built from the same commit as a different product
//     (2.12.0-hosted). A flavoured build is still a published one, naming one
//     commit, so a digest mismatch on it is corruption and not a new build. But
//     it is not a newer version of a standalone one, so lineages never compare:
//     letting a hosted tag win newestOf would point a cluster at an image whose
//     client has no `update`. The image build appends the flavour LAST, after
//     the revision (2.12.0+9f2a1c-hosted), and hosted() reads it as a suffix of
//     the whole string; both placements are read here.
//   - +REV: build metadata, not inspected. Releases before 2.4.1 were stamped
//     x.y.z+<rev>; a release already names one commit, so the suffix changes
//     nothing about what it is.
//
// Anything else ("dev", "dev+9f2a1c", "", "latest", a branch) is NOT a version,
// and every guard that reads one treats what it cannot read as RECENT. Refusing
// a feature to a build that has it is the costly mistake; skipping a guard aimed
// at agents that predate the feature is the cheap one.
//
// Display is separate: shortVersion renders for a human and keeps the flavour.
// Anywhere a version IDENTIFIES something (the core cache directory, a cached
// digest) the full string is what counts, and nothing here is consulted.

import "strings"

// semver is a parsed version. The package-level `version` is the launcher's own
// stamp, which is why the type does not carry that name.
type semver struct {
	major, minor, patch int
	hasPatch            bool   // x.y.z, rather than the x.y a stream tag stops at
	flavour             string // "" for the standalone lineage
	rev                 string // build metadata, kept for completeness only
}

// parseVersion reads s by the grammar above. ok is false for anything that is
// not a version, dev builds included.
func parseVersion(s string) (v semver, ok bool) {
	s = strings.TrimPrefix(s, "v")
	num, rev, _ := strings.Cut(s, "+")
	num, flavour, dashed := strings.Cut(num, "-")
	if !dashed {
		rev, flavour, dashed = strings.Cut(rev, "-")
	}
	if dashed && !flavourOK(flavour) {
		return semver{}, false
	}
	v.flavour, v.rev = flavour, rev
	parts := strings.Split(num, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return semver{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		n, ok := decimal(p)
		if !ok {
			return semver{}, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	v.hasPatch = len(parts) == 3
	return v, true
}

// flavourOK: a flavour is a lowercase word, letters then letters or digits.
// "hosted-something" is not one, so a build stamped that way is not read as a
// hosted release by accident; hosted() applies the same strictness to the
// suffix.
func flavourOK(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return s != ""
}

// decimal parses a non-empty run of digits. strconv.Atoi would also accept a
// sign, and "2.+3.0" is nobody's version.
func decimal(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// release reports whether v is a full x.y.z of the standalone lineage: what
// the registry is asked for, what a cluster is retargeted to, what two
// launchers are compared on.
func (v semver) release() bool { return v.hasPatch && v.flavour == "" }

// less reports v < o numerically on major, minor, patch. Two lineages never
// compare: the answer is false both ways, as it is for anything unparseable.
func (v semver) less(o semver) bool {
	if v.flavour != o.flavour {
		return false
	}
	switch {
	case v.major != o.major:
		return v.major < o.major
	case v.minor != o.minor:
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// before reports whether v predates maj.min, in any lineage: an old hosted
// core drops a profile key exactly as an old standalone one does.
func (v semver) before(maj, min int) bool {
	return v.major < maj || (v.major == maj && v.minor < min)
}

// versionBefore reports whether a RELEASED agent version predates maj.min.
// Non-semver versions ("dev+<rev>", "") are assumed current: a dev image is
// always built from a branch at least as new as this launcher.
func versionBefore(s string, maj, min int) bool {
	v, ok := parseVersion(s)
	return ok && v.before(maj, min)
}

// parseRelease reads s as a release, or reports that it is not one.
func parseRelease(s string) (semver, bool) {
	v, ok := parseVersion(s)
	if !ok || !v.release() {
		return semver{}, false
	}
	return v, true
}

// isRelease reports whether s is a released x.y.z (dev builds, streams and
// flavoured builds are not).
func isRelease(s string) bool {
	_, ok := parseRelease(s)
	return ok
}

// releaseLess reports a < b for two releases, false when either side is not
// one: never act on a comparison that means nothing.
func releaseLess(a, b string) bool {
	va, oka := parseRelease(a)
	vb, okb := parseRelease(b)
	return oka && okb && va.less(vb)
}

// shortVersion renders a version for a HUMAN. A release tag already designates
// one commit, so "2.4.0+983761c" only makes the version harder to read wherever
// it shows; a branch build keeps its revision, which is the only thing telling
// two of them apart. Releases have been stamped bare since 2.4.1, so this is
// what makes an agent from before that read the same as one from after. The
// flavour stays: dropping it would print a different product's name.
//
// Display only. Anywhere a version IDENTIFIES something (the core cache
// directory, any comparison) the full string is what counts.
func shortVersion(v string) string {
	if base, _, ok := strings.Cut(v, "+"); ok {
		if _, isVersion := parseVersion(base); isVersion {
			return base
		}
	}
	return v
}
