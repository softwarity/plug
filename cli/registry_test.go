package main

import (
	"strings"
	"testing"
)

// decideClient is the whole client-side lookup in one pure function; these are
// the outcomes `plug update` can reach without touching the cluster.
func TestDecideClient(t *testing.T) {
	tags := []string{"latest", "main", "2.5.2", "2.5.1", "2.4.0"}
	for _, c := range []struct {
		name, img, before, want string
		apply, current, errSub  string
		delegate                bool
	}{
		{"release pin, newer published", "softwarity/plug:2.5.1", "2.5.1", "", "2.5.2", "", "", false},
		{"release pin, already newest", "softwarity/plug:2.5.2", "2.5.2", "", "", "already the newest", "", false},
		{"digest-only pin follows releases", "softwarity/plug@sha256:abc", "2.5.1", "", "2.5.2", "", "", false},
		{"moving tag: cluster's call", "softwarity/plug:latest", "2.5.2", "", "", "", "", true},
		{"dev agent: cluster's call", "softwarity/plug:2.5.1", "dev", "", "", "", "", true},
		{"want tag → newest release", "softwarity/plug:latest", "2.5.2", "tag", "2.5.2", "", "", false},
		{"want a published stream", "softwarity/plug:2.5.2", "2.5.2", "main", "main", "", "", false},
		{"want an exact release", "softwarity/plug:2.5.2", "2.5.2", "2.4.0", "2.4.0", "", "", false},
		{"want an unpublished tag", "softwarity/plug:2.5.2", "2.5.2", "nope", "", "", "has no tag", false},
	} {
		apply, current, errMsg, delegate := decideClient(c.img, c.before, c.want, tags)
		if apply != c.apply || delegate != c.delegate ||
			(c.current == "") != (current == "") || (c.errSub == "") != (errMsg == "") {
			t.Errorf("%s: got (apply=%q current=%q err=%q delegate=%v)", c.name, apply, current, errMsg, delegate)
			continue
		}
		if c.current != "" && !strings.Contains(current, c.current) {
			t.Errorf("%s: current %q lacks %q", c.name, current, c.current)
		}
		if c.errSub != "" && !strings.Contains(errMsg, c.errSub) {
			t.Errorf("%s: err %q lacks %q", c.name, errMsg, c.errSub)
		}
	}
}

// The mirrored parseImageRef must agree with docker's defaulting.
func TestParseImageRefCLI(t *testing.T) {
	for _, c := range []struct{ ref, host, repo, tag string }{
		{"softwarity/plug:2.5.2", "registry-1.docker.io", "softwarity/plug", "2.5.2"},
		{"softwarity/plug@sha256:abc", "registry-1.docker.io", "softwarity/plug", "latest"},
		{"docker.io/softwarity/plug:2.5.2", "registry-1.docker.io", "softwarity/plug", "2.5.2"},
		{"registry.corp:5000/team/plug:dev", "registry.corp:5000", "team/plug", "dev"},
		{"redis", "registry-1.docker.io", "library/redis", "latest"},
	} {
		h, r, tg := parseImageRef(c.ref)
		if h != c.host || r != c.repo || tg != c.tag {
			t.Errorf("parseImageRef(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, h, r, tg, c.host, c.repo, c.tag)
		}
	}
}

// An agent stamped x.y.z+<rev> (every release before 2.4.1) must take the
// client-side fast path, not be mistaken for a dev build and delegated.
func TestDecideClientHandlesBuildMetadata(t *testing.T) {
	tags := []string{"latest", "2.5.4", "2.4.0"}
	apply, current, errMsg, delegate := decideClient("softwarity/plug:2.4.0", "2.4.0+983761c", "", tags)
	if delegate || errMsg != "" || current != "" || apply != "2.5.4" {
		t.Errorf("got (apply=%q current=%q err=%q delegate=%v), want apply=2.5.4", apply, current, errMsg, delegate)
	}
	// and when it is already the newest, say so instead of delegating
	_, current, _, delegate = decideClient("softwarity/plug:2.5.4", "2.5.4+abc1234", "", tags)
	if delegate || current == "" {
		t.Errorf("already-newest with +rev: current=%q delegate=%v", current, delegate)
	}
	if strings.Contains(current, "+abc1234") {
		t.Errorf("build metadata leaked into the message: %q", current)
	}
}
