//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The store holds the core that macOS executes AS ROOT, and the guard used to
// stop at the deepest component that already existed, on the reasoning that if
// that one is sound then what we create below it is ours. It only holds if the
// ancestors are sound too: a tidy root-owned directory sitting inside a
// world-writable one can be moved aside and replaced by whoever can write the
// parent, and the next launch runs what they put there.
func TestTheStoreGuardLooksAboveTheDeepestComponent(t *testing.T) {
	root := t.TempDir()
	loose := filepath.Join(root, "loose")
	if err := os.Mkdir(loose, 0o777); err != nil { // anyone can write here
		t.Fatal(err)
	}
	// Deliberately re-applied: MkdirAll obeys the umask, and the whole point of
	// this fixture is a directory that really is world-writable.
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	tidy := filepath.Join(loose, "versions")
	if err := os.Mkdir(tidy, 0o755); err != nil {
		t.Fatal(err)
	}

	var said string
	saved := guardFatal
	guardFatal = func(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }
	defer func() { guardFatal = saved }()

	func() {
		defer func() {
			if r := recover(); r != nil {
				said, _ = r.(string)
			}
		}()
		guardStoreOwnedBy(filepath.Join(tidy, "9.9.9", "plug"), uint32(os.Getuid()), root)
	}()

	if said == "" {
		t.Fatal("a tidy store inside a world-writable directory was accepted: anyone able to write " +
			"the parent can swap it and hand root a different binary")
	}
	if !strings.Contains(said, loose) {
		t.Errorf("the refusal does not name the directory at fault (%s): %s", loose, said)
	}
}

// And the REAL store must still pass, or macOS cannot launch at all. A fixture
// cannot stand in for this one: the walk now goes all the way to the filesystem
// root, and no test can build a chain of root-owned directories under a temp dir.
// So ask the question about the path plug actually uses, on the machine it is
// running on. That is the assertion that matters anyway: the walk was widened,
// and the thing to know is whether it now refuses a healthy install.
func TestTheRealStorePathIsAccepted(t *testing.T) {
	saved := guardFatal
	guardFatal = func(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }
	defer func() { guardFatal = saved }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the widened walk refuses a healthy macOS install, which would stop plug from launching: %v", r)
		}
	}()
	guardStoreOwnedBy(filepath.Join(versionsDir(), "9.9.9", "plug"), 0, "")
}
