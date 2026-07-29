package main

import "testing"

// retargetTo is what `plug update <tag>` resolves to. The cases that matter are
// the ones that would repoint a live deployment at something that cannot run.
func TestRetargetToPlans(t *testing.T) {
	tags := []string{"latest", "main", "feat-09", "2.4.1", "2.4.0", "2.4", "2"}
	for _, c := range []struct {
		name, img, want string
		target, plan    string
	}{
		{"newest release from a branch", "softwarity/plug:feat-09", "tag", "softwarity/plug:2.4.1", planRetarget},
		{"newest release picks x.y.z, not x.y", "softwarity/plug:latest", "tag", "softwarity/plug:2.4.1", planRetarget},
		{"switch to latest from a pin", "softwarity/plug:2.4.0", "latest", "softwarity/plug:latest", planRetarget},
		{"switch to a branch tag", "softwarity/plug:latest", "feat-09", "softwarity/plug:feat-09", planRetarget},
		{"a moving tag we already carry is re-resolved", "softwarity/plug:latest", "latest", "softwarity/plug:latest", planResolve},
		{"a release we already carry is a no-op", "softwarity/plug:2.4.1", "2.4.1", "softwarity/plug:2.4.1", planCurrent},
		{"newest release when already on it", "softwarity/plug:2.4.1", "tag", "softwarity/plug:2.4.1", planCurrent},
		{"a digest is dropped when retagging", "softwarity/plug:2.4.0@sha256:abc", "latest", "softwarity/plug:latest", planRetarget},
	} {
		target, plan, note := retargetToWith(c.img, c.want, tags, nil)
		if target != c.target || plan != c.plan {
			t.Errorf("%s: got (%q, %q) [%s], want (%q, %q)", c.name, target, plan, note, c.target, c.plan)
		}
	}
}

// A tag nobody published must never become the deployment's image: on Swarm or
// k8s that is a rollout to an image that cannot pull, unwound by hand.
func TestRetargetToRefusesUnknownTag(t *testing.T) {
	tags := []string{"latest", "2.4.1"}
	for _, want := range []string{"feat-99", "2.9.9", "typo"} {
		if _, plan, note := retargetToWith("softwarity/plug:latest", want, tags, nil); plan != "" {
			t.Errorf("%q was accepted (plan %q, %q) — it is not published", want, plan, note)
		}
	}
}

// No x.y.z published at all: `update tag` has nothing to aim at and must say so
// rather than fall back to something arbitrary.
func TestRetargetToNoReleasePublished(t *testing.T) {
	if _, plan, _ := retargetToWith("softwarity/plug:main", "tag", []string{"main", "latest"}, nil); plan != "" {
		t.Errorf("plan = %q, want refusal", plan)
	}
}

// A registry that cannot be listed cannot authorise a move: unlike `update`
// with no target, there is nothing safe to degrade to.
func TestRetargetToRefusesWhenRegistryUnreachable(t *testing.T) {
	if _, plan, _ := retargetToWith("softwarity/plug:latest", "tag", nil, errTest); plan != "" {
		t.Errorf("plan = %q, want refusal", plan)
	}
}

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "no route to registry" }
