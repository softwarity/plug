package agent

import (
	"errors"
	"reflect"
	"testing"
)

// `plug update` MOVES a pinned deployment to the newest release. Everything
// below decides which image a cluster ends up on, so a mistake here either
// strands a cluster on an old agent (the bug this feature fixes) or, worse,
// points a deployment at an image that does not exist.

func TestParseImageRef(t *testing.T) {
	for _, c := range []struct{ ref, host, repo, tag string }{
		// Docker Hub, the shape plug itself ships as.
		{"softwarity/plug:2.3.0", "registry-1.docker.io", "softwarity/plug", "2.3.0"},
		{"softwarity/plug", "registry-1.docker.io", "softwarity/plug", "latest"},
		// A single-part name lives under library/ on the Hub.
		{"nginx:1.29", "registry-1.docker.io", "library/nginx", "1.29"},
		// An explicit Hub host resolves to the API endpoint, not the web one.
		{"docker.io/softwarity/plug:2", "registry-1.docker.io", "softwarity/plug", "2"},
		{"index.docker.io/softwarity/plug:2", "registry-1.docker.io", "softwarity/plug", "2"},
		// A first component with a dot or a colon is a registry host — this is
		// what tells a private registry apart from a Hub namespace.
		{"ghcr.io/softwarity/plug:2.4.0", "ghcr.io", "softwarity/plug", "2.4.0"},
		{"registry.example.com/team/plug:1.0.0", "registry.example.com", "team/plug", "1.0.0"},
		{"localhost:5000/plug:dev", "localhost:5000", "plug", "dev"},
		// A port in the host must not be mistaken for the tag separator.
		{"myregistry:5000/team/plug", "myregistry:5000", "team/plug", "latest"},
	} {
		host, repo, tag := parseImageRef(c.ref)
		if host != c.host || repo != c.repo || tag != c.tag {
			t.Errorf("parseImageRef(%q) = %q, %q, %q — want %q, %q, %q",
				c.ref, host, repo, tag, c.host, c.repo, c.tag)
		}
	}
}

func TestRetagged(t *testing.T) {
	for _, c := range []struct{ in, tag, want string }{
		{"softwarity/plug:2.3.0", "2.4.0", "softwarity/plug:2.4.0"},
		{"softwarity/plug", "2.4.0", "softwarity/plug:2.4.0"},
		// A stack deploy pins a digest; it contradicts a new tag, so it goes.
		{"softwarity/plug:2.3.0@sha256:abc123", "2.4.0", "softwarity/plug:2.4.0"},
		{"ghcr.io/team/plug:1.0.0", "2.0.0", "ghcr.io/team/plug:2.0.0"},
		// The registry port must survive the retag.
		{"localhost:5000/plug:1.0.0", "2.0.0", "localhost:5000/plug:2.0.0"},
	} {
		if got := retagged(c.in, c.tag); got != c.want {
			t.Errorf("retagged(%q, %q) = %q, want %q", c.in, c.tag, got, c.want)
		}
	}
}

// The release/moving split is the whole policy: a release tag gets moved, a
// moving tag is left to its publisher.
func TestIsReleaseTag(t *testing.T) {
	for _, tag := range []string{"2", "2.3", "2.3.0", "v2.3.0", "10.20.30"} {
		if !isReleaseTag(tag) {
			t.Errorf("%q should be a release tag", tag)
		}
	}
	for _, tag := range []string{"latest", "main", "dev", "feature-x", "2.3.0-rc1", "edge", ""} {
		if isReleaseTag(tag) {
			t.Errorf("%q should be a moving tag", tag)
		}
	}
}

// Only a full x.y.z is worth retargeting TO — aiming at "2" would swap a pin
// for a pin that lies about what runs.
func TestParseExactRelease(t *testing.T) {
	v, ok := parseExactRelease("2.4.0")
	if !ok || v != [3]int{2, 4, 0} {
		t.Fatalf("parseExactRelease(2.4.0) = %v, %v", v, ok)
	}
	for _, tag := range []string{"2", "2.4", "latest", "2.4.0-rc1", "2.4.0+abc"} {
		if _, ok := parseExactRelease(tag); ok {
			t.Errorf("%q must not parse as an exact release", tag)
		}
	}
}

func TestReleaseNewerThan(t *testing.T) {
	// Numeric per part — a string compare would rank 2.10.0 below 2.9.0.
	if !releaseNewerThan("2.10.0", "2.9.0") {
		t.Error("2.10.0 must be newer than 2.9.0")
	}
	if !releaseNewerThan("3.0.0", "2.4.0") {
		t.Error("a new major must count as newer — the update crosses majors by design")
	}
	// The running version carries +<rev> build metadata.
	if !releaseNewerThan("2.4.0", "2.3.0+bb03611") {
		t.Error("build metadata on the running version must not block the comparison")
	}
	if releaseNewerThan("2.3.0", "2.3.0+bb03611") {
		t.Error("the same release must not count as newer")
	}
	// Never downgrade: a registry whose tags were pruned, or a lagging mirror,
	// must not drag a cluster backwards.
	if releaseNewerThan("2.3.0", "2.4.0") {
		t.Error("an older release must never be retargeted to")
	}
	// A dev agent takes any release — it is not on a release line at all.
	if !releaseNewerThan("2.4.0", "dev+1ca6a07") {
		t.Error("a dev agent must accept a release")
	}
	if releaseNewerThan("latest", "2.3.0") {
		t.Error("a non-release candidate must never be retargeted to")
	}
}

func TestRetargetPlan(t *testing.T) {
	// The case this whole feature exists for: a pinned release, a newer one
	// published. Before, re-resolving 2.3.0 returned 2.3.0 forever.
	target, plan, note := retargetPlan("softwarity/plug:2.3.0", "2.3.0+bb03611", "2.4.0", nil)
	if target != "softwarity/plug:2.4.0" || plan != planRetarget {
		t.Fatalf("target = %q, plan = %q, note = %q", target, plan, note)
	}

	// Pinned and already newest: answer immediately. Rolling the task would
	// make the CLI poll 90s for a version that cannot change.
	target, plan, _ = retargetPlan("softwarity/plug:2.4.0", "2.4.0", "2.4.0", nil)
	if plan != planCurrent || target != "softwarity/plug:2.4.0" {
		t.Fatalf("an up-to-date pin must report current, got %q / %q", plan, target)
	}

	// A moving tag belongs to its publisher — never retargeted, just re-pulled.
	for _, img := range []string{"softwarity/plug:latest", "softwarity/plug:main", "softwarity/plug:my-branch"} {
		target, plan, _ = retargetPlan(img, "2.3.0", "2.4.0", nil)
		if plan != planResolve || target != img {
			t.Errorf("%s: plan = %q, target = %q — a moving tag must be left alone", img, plan, target)
		}
	}

	// A registry that cannot be listed degrades to the old behaviour instead of
	// blocking the update, and says why.
	target, plan, note = retargetPlan("softwarity/plug:2.3.0", "2.3.0", "", errors.New("registry answered 500"))
	if plan != planResolve || target != "softwarity/plug:2.3.0" {
		t.Fatalf("a listing failure must degrade, got %q / %q", plan, target)
	}
	if !contains(note, "registry answered 500") {
		t.Errorf("the note must carry the registry's reason, got %q", note)
	}

	// A digest pin is dropped on the way — it would otherwise override the tag.
	target, plan, _ = retargetPlan("softwarity/plug:2.3.0@sha256:deadbeef", "2.3.0", "2.4.0", nil)
	if target != "softwarity/plug:2.4.0" || plan != planRetarget {
		t.Fatalf("target = %q, plan = %q", target, plan)
	}
}

// A scope legitimately contains commas (repository:x/y:pull,push), so the
// challenge cannot be split naively — a wrong split loses the scope and the
// token comes back unusable.
func TestSplitChallenge(t *testing.T) {
	got := splitChallenge(`realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:softwarity/plug:pull,push"`)
	want := []string{
		`realm="https://auth.docker.io/token"`,
		`service="registry.docker.io"`,
		`scope="repository:softwarity/plug:pull,push"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitChallenge = %q, want %q", got, want)
	}
}

func TestParseNextLink(t *testing.T) {
	if got := parseNextLink(`</v2/softwarity/plug/tags/list?n=100&last=2.3.0>; rel="next"`); got != "/v2/softwarity/plug/tags/list?n=100&last=2.3.0" {
		t.Fatalf("parseNextLink = %q", got)
	}
	if got := parseNextLink(`</v2/x/tags/list>; rel="prev"`); got != "" {
		t.Fatalf("only rel=next counts, got %q", got)
	}
	if got := parseNextLink(""); got != "" {
		t.Fatalf("parseNextLink(\"\") = %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
