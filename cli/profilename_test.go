package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A profile name becomes a file name under ~/.plug, and plug acts on it while
// holding root. filepath.Join RESOLVES "..", so anything that can carry a
// separator escapes the directory — `plug rn mine ../../../etc/ssh/
// sshd_config.d/x` would land a caller-written file in a root-only directory.
func TestCheckProfileNameRejectsAnythingThatEscapesPlugDir(t *testing.T) {
	escapes := []string{
		"../../../etc/ssh/sshd_config.d/evil",
		"../../.ssh/authorized_keys",
		"..",
		".",
		"sub/dir",
		`win\dir`,
		"/etc/passwd",
		"",
		".hidden",               // a leading dot is how "../" starts
		"-flag",                 // never let a name read as an option
		strings.Repeat("a", 64), // over the label budget
	}
	for _, name := range escapes {
		if err := checkProfileName(name); err == nil {
			t.Errorf("checkProfileName(%q) = nil, want a rejection", name)
			continue
		}
		// Belt and braces: prove the name really would have escaped.
		if joined := filepath.Join("/home/me/.plug", name+".conf"); name == escapes[0] &&
			!strings.HasPrefix(joined, "/etc/") {
			t.Errorf("expected %q to escape, Join gave %s", name, joined)
		}
	}
}

func TestCheckProfileNameAcceptsTheOrdinaryOnes(t *testing.T) {
	for _, name := range []string{"neo", "staging", "prod-eu", "cluster_2", "v2.1", "a", strings.Repeat("a", 63)} {
		if err := checkProfileName(name); err != nil {
			t.Errorf("checkProfileName(%q) = %v, want accepted", name, err)
		}
	}
}
