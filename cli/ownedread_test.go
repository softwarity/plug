//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard answers a question about a PATH and the caller then opens that path
// again. Between the two, anything running as the user can swap the final
// component for a symlink to a file only root can read, and plug, which is setuid
// on macOS, opens it as root and offers it to whatever server the caller named.
//
// A symlink is refused outright at the open, so the identity checked and the
// bytes returned cannot be two different files.
func TestAKeyPathThatBecameASymlinkIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "swapped")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here (%v)", err)
	}
	if _, err := readUserOwnedFile(link); err == nil {
		t.Fatal("a symlink was followed. That is the swap this exists to refuse: the path checked and " +
			"the file opened are then two different things, and the second one is opened with plug's " +
			"privilege rather than the caller's")
	} else if !strings.Contains(err.Error(), "symbolic link") && !strings.Contains(err.Error(), "too many levels") {
		t.Logf("refused, for: %v", err)
	}
}

// And a real file the user owns must still be read, or no personal key works.
func TestAKeyTheUserOwnsIsStillRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := readUserOwnedFile(path)
	if err != nil {
		t.Fatalf("a file this user owns was refused: %v", err)
	}
	if string(b) != "a key" {
		t.Errorf("read %q, want the file's own bytes", b)
	}
}

// A path that does not exist must fail as a missing file, not as a privilege
// problem: the message the user sees decides whether they look at their profile
// or at their permissions.
func TestAMissingKeyFailsAsMissing(t *testing.T) {
	_, err := readUserOwnedFile(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("a missing file was read")
	}
	if !os.IsNotExist(err) {
		t.Errorf("a missing file reported %v, which sends the reader looking in the wrong place", err)
	}
}
