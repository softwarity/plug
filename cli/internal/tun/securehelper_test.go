//go:build !windows

package tun

// The privileged helpers plug runs must come from somewhere root owns.
//
// run() attaches plug's capabilities to whatever it starts, and every caller
// names its helper bare. The launcher's $PATH narrowing was supposed to cover
// this and does not on Linux: it returns early when euid equals ruid, which is
// exactly what file capabilities give you. So the lookup happens here instead,
// where the exec is, and cannot be skipped by a condition somewhere else.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAHelperIsResolvedUnderARootOwnedDirectory(t *testing.T) {
	// `sh` exists on every unix this runs on, in one of the listed directories.
	got, ok := helperPath("sh")
	if !ok {
		t.Fatalf("sh was not found in %s", helperDirsList())
	}
	if !filepath.IsAbs(got) {
		t.Errorf("helperPath returned %q, which is not an absolute path", got)
	}
	var under bool
	for _, d := range helperDirs {
		if filepath.Dir(got) == d {
			under = true
		}
	}
	if !under {
		t.Errorf("helperPath returned %q, outside %s", got, helperDirsList())
	}
}

// The whole point: a helper planted on $PATH must not win. This is the attack
// the audit describes, reproduced as closely as a test can without being root.
func TestAHelperOnThePathIsIgnored(t *testing.T) {
	evil := t.TempDir()
	planted := filepath.Join(evil, "plug-test-helper")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", evil+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got, ok := helperPath("plug-test-helper"); ok {
		t.Errorf("helperPath found %q by consulting $PATH: that is the hole this closes", got)
	}
	// And `sh` still resolves to the system one, not to anything on $PATH.
	got, ok := helperPath("sh")
	if !ok || strings.HasPrefix(got, evil) {
		t.Errorf("sh resolved to %q (ok=%v), want the system one", got, ok)
	}
}

// An absolute name is taken as given: an operator who moved a tool on purpose
// said where it is, and second-guessing them would break that machine.
func TestAnAbsoluteHelperIsTakenAsGiven(t *testing.T) {
	if got, ok := helperPath("/opt/custom/ip"); !ok || got != "/opt/custom/ip" {
		t.Errorf("helperPath(/opt/custom/ip) = %q,%v", got, ok)
	}
}

// A helper nobody can find fails with the directories named. Someone on a layout
// plug did not anticipate has to be able to see why before they can say so.
func TestAMissingHelperSaysWhereItLooked(t *testing.T) {
	err := run("plug-definitely-not-a-real-helper", "--version")
	if err == nil {
		t.Fatal("a helper that does not exist must not be run")
	}
	for _, want := range []string{"plug-definitely-not-a-real-helper", "/usr/sbin", "$PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
