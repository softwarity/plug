//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The child's command is resolved against the human's PATH, not plug's.
// Unix-only like its subject: lookPathIn runs only after securePath narrowed
// $PATH, which needs setuid/caps — never the case on Windows.
func TestLookPathIn(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mytool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExe := filepath.Join(dir, "readme")
	if err := os.WriteFile(notExe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := lookPathIn("mytool", "/nonexistent:"+dir); err != nil || got != exe {
		t.Errorf("lookPathIn = (%q,%v), want %q", got, err, exe)
	}
	if _, err := lookPathIn("readme", dir); err == nil {
		t.Error("a non-executable file must not resolve")
	}
	if _, err := lookPathIn("absent", dir); err == nil {
		t.Error("an absent file must not resolve")
	}
	if got, _ := lookPathIn("/bin/sh", ""); got != "/bin/sh" {
		t.Errorf("an explicit path must pass through, got %q", got)
	}
}
