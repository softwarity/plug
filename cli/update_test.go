package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSemver(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"2.2.0", "2.3.0", true},
		{"2.3.0", "2.2.0", false},
		{"2.2.0", "2.2.0", false},
		{"2.9.9", "2.10.0", true}, // numeric, not lexical
		{"2.2.0", "3.0.0", true},
		{"dev+abc", "2.3.0", false}, // a dev side means the comparison is void
		{"2.2.0", "dev+abc", false},
		// Published images stamp VERSION+GIT_REV — the metadata must not turn a
		// release into a "dev build" (bench-caught against the real 2.2.0 image).
		{"2.2.0+3503368", "2.3.0+abc1234", true},
		{"2.3.0+abc1234", "2.2.0+3503368", false},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.less {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	if semverOK("dev+abc") || semverOK("2.2") || !semverOK("2.2.0") || !semverOK("2.2.0+3503368") {
		t.Error("semverOK: dev/short accepted, or release refused")
	}
}

func TestUpdateWord(t *testing.T) {
	cases := map[string]string{
		"updating service plug — rolling":                    "updating",
		"current v2.2.0 — image softwarity/plug:2 unchanged": "current",
		"error: unknown command \"self-update\"":             "error:",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := updateWord(in); got != want {
			t.Errorf("updateWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMaxCachedRelease pins the stale-launcher hint's source of truth: newest
// RELEASED core in the cache, numeric compare, dev builds ignored.
func TestMaxCachedRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	if got := maxCachedRelease(); got != "" {
		t.Fatalf("empty cache: got %q", got)
	}
	for _, d := range []string{"dev+abc", "2.2.0+x", "2.10.0+y", "not-a-version"} {
		if err := os.MkdirAll(filepath.Join(home, ".plug", "versions", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := maxCachedRelease(); got != "2.10.0+y" {
		t.Fatalf("got %q, want 2.10.0+y (numeric compare, dev ignored)", got)
	}
}

// TestReplaceBinary exercises the swap on every OS (Windows included in CI):
// the target must hold the new bytes afterwards, with no .old left behind on a
// non-running file.
func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "plug-bin")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("new-binary-content")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new-binary-content" {
		t.Fatalf("target holds %q (%v), want the new content", got, err)
	}
	if _, err := os.Stat(target + ".old"); err == nil {
		t.Error("a .old copy survived on an unlocked file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover temp files in %s: %d entries", dir, len(entries))
	}
}
