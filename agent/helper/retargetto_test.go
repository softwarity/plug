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

// applyPlan trusts the caller's registry check and must never look anything up
// — what it decides from the image alone is all it has.
func TestApplyPlan(t *testing.T) {
	for _, c := range []struct{ img, tag, target, plan string }{
		{"softwarity/plug:2.5.1", "2.5.2", "softwarity/plug:2.5.2", planRetarget},
		{"softwarity/plug:2.5.2", "2.5.2", "softwarity/plug:2.5.2", planCurrent},
		{"softwarity/plug:latest", "latest", "softwarity/plug:latest", planResolve},
		{"softwarity/plug:2.5.1", "latest", "softwarity/plug:latest", planRetarget},
		// a digest-only pin never equals any tag — always a switch
		{"softwarity/plug@sha256:abc", "2.5.2", "softwarity/plug:2.5.2", planRetarget},
		{"softwarity/plug:2.5.1@sha256:abc", "2.5.2", "softwarity/plug:2.5.2", planRetarget},
		{"docker.io/softwarity/plug:2.5.1", "2.5.2", "docker.io/softwarity/plug:2.5.2", planRetarget},
	} {
		target, plan, _ := applyPlan(c.tag)(c.img)
		if target != c.target || plan != c.plan {
			t.Errorf("applyPlan(%q)(%q) = (%q,%q), want (%q,%q)", c.tag, c.img, target, plan, c.target, c.plan)
		}
	}
}

func TestHasTag(t *testing.T) {
	for _, c := range []struct {
		ref  string
		want bool
	}{
		{"softwarity/plug:2.5.1", true},
		{"softwarity/plug", false},
		{"softwarity/plug@sha256:abc", false},
		{"softwarity/plug:2.5.1@sha256:abc", true},
		{"localhost:5000/plug", false}, // the colon is the registry port
		{"localhost:5000/plug:dev", true},
	} {
		if got := hasTag(c.ref); got != c.want {
			t.Errorf("hasTag(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

// signpostAgentPort digs the relay port out of the two signpost shapes the
// backends create — the collision guard depends on it.
func TestSignpostAgentPort(t *testing.T) {
	for _, c := range []struct {
		cmd  []string
		want string
	}{
		{[]string{"/usr/local/bin/plug-agent", "signpost", "3000", "neo_plug:41234"}, "41234"},
		{[]string{"/usr/local/bin/plug-agent", "signpost", "3000", "10.0.1.5:52801"}, "52801"},
		// multi-port signpost (HTTP+SMTP+POP3 on one name): first pair decides
		{[]string{"/usr/local/bin/plug-agent", "signpost", "80", "neo_plug:41001", "25", "neo_plug:41002", "425", "neo_plug:41003"}, "41001"},
		{[]string{"sleep", "60"}, ""},
		{nil, ""},
		{[]string{"signpost"}, ""},
	} {
		if got := signpostAgentPort(c.cmd); got != c.want {
			t.Errorf("signpostAgentPort(%v) = %q, want %q", c.cmd, got, c.want)
		}
	}
}
