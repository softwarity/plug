package agent

import "testing"

// The digest must match the image's own repository: an image retagged across
// repos carries several RepoDigests, and picking a stranger's would make the
// manager pull a different repo than the one deployed.
func TestDigestFor(t *testing.T) {
	digests := []string{
		"registry.corp/mirror/plug@sha256:aaa",
		"softwarity/plug@sha256:bbb",
	}
	for _, c := range []struct{ img, want string }{
		{"softwarity/plug:2.5.0", "softwarity/plug@sha256:bbb"},
		{"registry.corp/mirror/plug:2.5.0", "registry.corp/mirror/plug@sha256:aaa"},
		{"softwarity/plug", "softwarity/plug@sha256:bbb"}, // no tag
		{"other/repo:1.0", ""}, // no digest for that repo
	} {
		if got := digestFor(c.img, digests); got != c.want {
			t.Errorf("digestFor(%q) = %q, want %q", c.img, got, c.want)
		}
	}
	// A locally built image has no RepoDigests at all — the caller falls back
	// to the bare tag (the CI's :e2e image takes this path).
	if got := digestFor("softwarity/plug:e2e", nil); got != "" {
		t.Errorf("digestFor with no RepoDigests = %q, want empty", got)
	}
}
