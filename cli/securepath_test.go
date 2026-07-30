package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plug narrows its own $PATH when privileged, but the command you launch must
// keep yours — otherwise `plug -s … npm run dev` stops finding npm.
func TestWithUserPathRestoresTheHumansPath(t *testing.T) {
	saved := userPath
	userPath = "/home/me/.nvm/versions/node/v22/bin:/usr/local/bin"
	defer func() { userPath = saved }()

	got := withUserPath([]string{"HOME=/home/me", "PATH=/usr/sbin:/usr/bin", "TERM=xterm"})
	var paths []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want exactly one PATH entry, got %v", paths)
	}
	if paths[0] != "PATH="+userPath {
		t.Errorf("child PATH = %q, want the human's %q", paths[0], userPath)
	}
	for _, want := range []string{"HOME=/home/me", "TERM=xterm"} {
		if !contains(got, want) {
			t.Errorf("withUserPath dropped %q", want)
		}
	}
	// nil env means "inherit", and must still carry the human's PATH
	if !contains(withUserPath(nil), "PATH="+userPath) {
		t.Error("withUserPath(nil) lost the human's PATH")
	}
}

// The child's command is resolved against the human's PATH, not plug's.
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

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
