package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardStorePath is what the whole macOS arrangement rests on: the core is run
// as root, so the directory holding it must not be writable by anyone else. A
// parent someone can write is a parent someone can replace with a symlink, and
// the store would be back within reach without a single permission ON the store
// looking wrong. So the check walks up, and these tests walk up with it.
//
// fatal() exits the process, so the refusals cannot be asserted in-process.
// What is asserted here is the decision itself, through the same predicate the
// guard uses — and the message, which is the part a user has to act on.
func TestStoreOwnershipMessageSaysWhatToRun(t *testing.T) {
	msg := storeOwnershipMsg("/var/db/plug", "it is owned by uid 501, not root")
	for _, want := range []string{"/var/db/plug", "owned by uid 501", "sudo chown root:wheel", "sudo chmod 755"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not contain %q — a user cannot act on it:\n%s", want, msg)
		}
	}
}

// The real system paths, on the machine running the tests: /var/db must be
// root-owned and not writable by group or others, or the store's parent is not
// the anchor this design assumes. Worth asserting rather than believing: it is
// exactly the assumption that fails on an Intel Mac where Homebrew owns
// /usr/local, which is why the store is not there.
func TestTheStoreAnchorIsRootOwnedOnThisMachine(t *testing.T) {
	anchor := filepath.Dir(filepath.Dir(versionsDir())) // /var/db
	fi, err := os.Stat(anchor)
	if err != nil {
		t.Skipf("%s is not present on this machine", anchor)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("%s is writable by group or others (%v) — the core store cannot be anchored there",
			anchor, fi.Mode().Perm())
	}
}

// A cached core is disposable — re-downloaded on demand, verified on every
// launch — so nothing is migrated when the store moves. The one thing that must
// not happen is the old place being left for a prune that no longer looks at
// it, so it has to be NAMED somewhere, and it must not be the new one.
func TestTheOldStoreIsStillNamedSoItCanBeCleaned(t *testing.T) {
	old := legacyVersionsDir()
	if old == "" {
		t.Fatal("macOS moved its store, so the old location must still be named for prune and uninstall to clear it")
	}
	if old == versionsDir() {
		t.Fatalf("the old and new stores are the same path (%s) — one of them is wrong", old)
	}
	if !strings.HasPrefix(old, plugDir()) {
		t.Errorf("the old store %q was under the user's ~/.plug — that is what it must still point at", old)
	}
}
