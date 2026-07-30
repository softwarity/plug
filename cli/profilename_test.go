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
	// Names that WALK OUT of ~/.plug. Each is checked twice: rejected by the
	// validator, and — belt and braces — actually escaping, so the test keeps
	// proving why the validator has to exist.
	base := filepath.Join(string(filepath.Separator), "home", "me", ".plug")
	for _, name := range []string{
		"../../../etc/ssh/sshd_config.d/evil",
		"../../.ssh/authorized_keys",
	} {
		if err := checkProfileName(name); err == nil {
			t.Errorf("checkProfileName(%q) = nil, want a rejection", name)
		}
		joined := filepath.Join(base, name+".conf")
		if strings.HasPrefix(joined, base+string(filepath.Separator)) {
			t.Errorf("%q was expected to escape %s, Join stayed inside: %s", name, base, joined)
		}
	}
	// Rejected without walking out: a leading "../" is what actually escapes —
	// ".." alone becomes "...conf", and Join re-roots "/etc/passwd" UNDER the
	// directory. They are refused for being paths (or unusable names) at all.
	for _, name := range []string{
		"..",
		"sub/dir",
		`win\dir`,
		"/etc/passwd",
		"",
		".",
		".hidden",               // a leading dot is how "../" starts
		"-flag",                 // never let a name read as an option
		strings.Repeat("a", 64), // over the label budget
	} {
		if err := checkProfileName(name); err == nil {
			t.Errorf("checkProfileName(%q) = nil, want a rejection", name)
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
