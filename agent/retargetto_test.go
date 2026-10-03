package agent

import (
	"strings"
	"testing"
)

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

// applyPlanWith is what `self-update apply <tag>` decides. The caller (the
// CLI) already checked the tag, and the agent checks it AGAIN, because the verb
// is reachable by anything that reaches the port: an unpublished tag is an
// agent that cannot pull, and an older release is a way back to what it fixed.
func TestApplyPlan(t *testing.T) {
	tags := []string{"latest", "main", "2.5.0", "2.5.1", "2.5.2", "2.4", "2", "1.9.0"}
	for _, c := range []struct{ img, tag, running, target, plan string }{
		{"softwarity/plug:2.5.1", "2.5.2", "2.5.1+abc", "softwarity/plug:2.5.2", planRetarget},
		{"softwarity/plug:2.5.2", "2.5.2", "2.5.2", "softwarity/plug:2.5.2", planCurrent},
		{"softwarity/plug:latest", "latest", "2.5.2", "softwarity/plug:latest", planResolve},
		// A channel switch to a moving tag is the caller's choice, up or down.
		{"softwarity/plug:2.5.1", "latest", "2.5.1", "softwarity/plug:latest", planRetarget},
		{"softwarity/plug:2.5.1", "main", "2.5.1", "softwarity/plug:main", planRetarget},
		// a digest-only pin never equals any tag - always a switch
		{"softwarity/plug@sha256:abc", "2.5.2", "2.5.1", "softwarity/plug:2.5.2", planRetarget},
		{"softwarity/plug:2.5.1@sha256:abc", "2.5.2", "2.5.1", "softwarity/plug:2.5.2", planRetarget},
		{"docker.io/softwarity/plug:2.5.1", "2.5.2", "2.5.1", "docker.io/softwarity/plug:2.5.2", planRetarget},
		// A short release tag that is not behind the running version is fine.
		{"softwarity/plug:2.5.1", "2", "2.5.1", "softwarity/plug:2", planRetarget},
		// A dev agent is on no release line: any release is a step forward.
		{"softwarity/plug:main", "1.9.0", "dev+1ca6a07", "softwarity/plug:1.9.0", planRetarget},
	} {
		target, plan, note := applyPlanWith(c.img, c.tag, c.running, false, tags, nil)
		if target != c.target || plan != c.plan {
			t.Errorf("applyPlanWith(%q, %q, running %q) = (%q,%q) [%s], want (%q,%q)", c.img, c.tag, c.running, target, plan, note, c.target, c.plan)
		}
	}
}

// A tag the registry does not list is refused, force or not: there is no
// deliberate way to want an image that does not exist.
func TestApplyPlanRefusesUnknownTag(t *testing.T) {
	tags := []string{"latest", "2.5.2"}
	for _, force := range []bool{false, true} {
		for _, tag := range []string{"2.9.9", "feat-99", "typo"} {
			if _, plan, note := applyPlanWith("softwarity/plug:2.5.2", tag, "2.5.2", force, tags, nil); plan != "" {
				t.Errorf("force=%v: %q was accepted (plan %q, %q) - it is not published", force, tag, plan, note)
			} else if !strings.Contains(note, tag) {
				t.Errorf("the refusal must name the tag, got %q", note)
			}
		}
	}
}

// A registry that cannot be listed cannot authorise the apply either: the
// whole request is "put me on that tag", and unchecked is the failure mode.
func TestApplyPlanRefusesWhenRegistryUnreachable(t *testing.T) {
	if _, plan, note := applyPlanWith("softwarity/plug:2.5.1", "2.5.2", "2.5.1", true, nil, errTest); plan != "" {
		t.Errorf("plan = %q, want refusal", plan)
	} else if !strings.Contains(note, "no route to registry") {
		t.Errorf("the refusal must carry the registry's reason, got %q", note)
	}
}

// A release older than the running one is a rollback, refused unless the
// caller says `force`; the refusal tells how. Equal is "current", not a
// rollback, and a newer one is simply applied.
func TestApplyPlanRefusesRollbackWithoutForce(t *testing.T) {
	tags := []string{"2.4.0", "2.4", "2.5.0", "2.5.1", "latest"}
	for _, tag := range []string{"2.5.0", "2.4.0", "2.4"} {
		_, plan, note := applyPlanWith("softwarity/plug:2.5.1", tag, "2.5.1+bb03611", false, tags, nil)
		if plan != "" {
			t.Errorf("%q was applied over a running 2.5.1 (plan %q) - a rollback must be refused", tag, plan)
			continue
		}
		if !strings.Contains(note, "self-update apply "+tag+" "+applyForceWord) {
			t.Errorf("the refusal must say how to force it, got %q", note)
		}
		target, plan, _ := applyPlanWith("softwarity/plug:2.5.1", tag, "2.5.1+bb03611", true, tags, nil)
		if plan != planRetarget || target != "softwarity/plug:"+tag {
			t.Errorf("with force, %q must be applied, got (%q, %q)", tag, target, plan)
		}
	}
	// The pin already carried is current, whatever the running version says.
	if _, plan, _ := applyPlanWith("softwarity/plug:2.5.0", "2.5.0", "2.5.1", false, tags, nil); plan != planCurrent {
		t.Errorf("the tag already carried is current, got %q", plan)
	}
}

func TestReleaseOlderThan(t *testing.T) {
	for _, c := range []struct {
		tag, running string
		want         bool
	}{
		{"2.5.0", "2.5.1", true},
		{"2.4.9", "2.5.1+abc", true},
		{"1.9.9", "2.0.0", true},
		{"2.5.1", "2.5.1", false},
		{"2.5.2", "2.5.1", false},
		{"2.10.0", "2.9.0", false}, // numeric, not lexical
		{"v2.4.0", "2.5.1", true},
		// Short tags compare on the parts they have.
		{"2.4", "2.5.1", true},
		{"2.5", "2.5.1", false},
		{"2", "2.5.1", false},
		{"1", "2.5.1", true},
		{"3", "2.5.1", false},
		// Moving tags and dev agents are never a rollback.
		{"latest", "2.5.1", false},
		{"main", "2.5.1", false},
		{"2.4.0", "dev+1ca6a07", false},
		{"2.4.0", "unknown", false},
	} {
		if got := releaseOlderThan(c.tag, c.running); got != c.want {
			t.Errorf("releaseOlderThan(%q, %q) = %v, want %v", c.tag, c.running, got, c.want)
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

// signpostRelay digs the relay ADDRESS out of the two signpost shapes the
// backends create, and ownerPort the port half of it - the collision guard
// depends on both. It reads a signpost an older agent created, which is the only
// reason the command is still parsed at all now that the address is a label.
func TestSignpostRelay(t *testing.T) {
	for _, c := range []struct {
		cmd            []string
		want, wantPort string
	}{
		{[]string{"/usr/local/bin/plug-agent", "signpost", "3000", "shop_plug:41234"}, "shop_plug:41234", "41234"},
		{[]string{"/usr/local/bin/plug-agent", "signpost", "3000", "10.0.1.5:52801"}, "10.0.1.5:52801", "52801"},
		// multi-port signpost (HTTP+SMTP+POP3 on one name): first pair decides
		{[]string{"/usr/local/bin/plug-agent", "signpost", "80", "shop_plug:41001", "25", "shop_plug:41002", "425", "shop_plug:41003"}, "shop_plug:41001", "41001"},
		{[]string{"sleep", "60"}, "", ""},
		{nil, "", ""},
		{[]string{"signpost"}, "", ""},
	} {
		got := signpostRelay(c.cmd)
		if got != c.want {
			t.Errorf("signpostRelay(%v) = %q, want %q", c.cmd, got, c.want)
		}
		if p := ownerPort(got); p != c.wantPort {
			t.Errorf("ownerPort(%q) = %q, want %q", got, p, c.wantPort)
		}
	}
}

// A signpost created by THIS version records its owner as a label; one created
// before it records the same address in its command, and must not read as
// ownerless just because the label is missing (every running session would look
// like a leftover the first time an upgraded agent boots).
func TestSignpostOwnerPrefersTheLabelAndFallsBackToTheCommand(t *testing.T) {
	cmd := []string{"/usr/local/bin/plug-agent", "signpost", "3000", "shop_plug.1.abc:41234"}
	if got := signpostOwner(map[string]string{sessionOwnerLabel: "shop_plug.2.def:52801"}, cmd); got != "shop_plug.2.def:52801" {
		t.Errorf("the label is the owner when it is there, got %q", got)
	}
	if got := signpostOwner(nil, cmd); got != "shop_plug.1.abc:41234" {
		t.Errorf("an older signpost's owner is its relay address, got %q", got)
	}
	if got := signpostOwner(nil, []string{"sleep", "60"}); got != "" {
		t.Errorf("nothing to read is nobody, got %q", got)
	}
}
