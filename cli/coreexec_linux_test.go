package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The gap this closes, reproduced: verify a file, have it replaced at its path,
// and run what was verified anyway.
//
// plug runs the cached core with the privilege it holds — ambient capabilities
// on Linux, root on macOS. If the check reads a PATH and the exec re-reads that
// same path, whatever wins the race between them runs privileged, and what can
// write into the cache is anything running as the user: the postinstall of the
// project plug is launching, for one.
//
// This test does not exercise plug's own launcher (that needs a cluster and a
// privileged install). It exercises the PRIMITIVE the launcher relies on, which
// is where the property actually lives: a descriptor is bound to an inode, so
// replacing the file at its path afterwards reaches a different file.
func TestExecTargetRunsTheVerifiedBytesNotThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core")

	if err := os.WriteFile(path, []byte("#!/bin/sh\necho VERIFIED\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// What the launcher does: open, and from here on speak only of this
	// descriptor.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The swap, after the check and before the run — atomically, the way an
	// attacker would rather than by truncating in place.
	other := filepath.Join(dir, "swapped")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho SUBSTITUTED\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}

	target, extra := execTarget(f)
	cmd := exec.Command(target)
	cmd.ExtraFiles = extra
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the verified descriptor failed: %v (%s)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "VERIFIED" {
		t.Fatalf("ran %q — the substituted file won the race, which is the whole defect", got)
	}

	// And the counter-proof, so the test cannot pass for the wrong reason: by
	// PATH, the substitution DOES take. If this ever prints VERIFIED, the swap
	// above stopped working and the assertion before it proves nothing.
	byPath, err := exec.Command(path).CombinedOutput()
	if err != nil {
		t.Fatalf("running by path failed: %v (%s)", err, byPath)
	}
	if got := strings.TrimSpace(string(byPath)); got != "SUBSTITUTED" {
		t.Fatalf("by path ran %q, want SUBSTITUTED — the swap did not happen, so this test proves nothing", got)
	}
}
