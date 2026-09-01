package main

import (
	"fmt"
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
// This test covers the message only, which is the part a user has to act on.
// The refusals themselves are taken further down, through the guard's own walk:
// they used to be described here as asserted "through the same predicate the
// guard uses", which was not true of anything in this file. Nothing reached
// them, and both fatal branches sat at zero hits.
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

// Every way the store can end up within someone's reach, taken through the
// guard's own walk. The failure they prevent is the same one each time: the
// cached core is run AS ROOT and is not verified against the directory it came
// from, so anyone who can write any component of that path can hand root a
// binary of their choosing, and no permission on the store itself looks wrong.
//
// The owner is passed in rather than being root, because a test running as a
// normal user cannot create a root-owned directory. What is exercised is the
// comparison, on real directories, with real modes: the production entry point
// pins the owner to root, and TestTheStoreGuardDemandsRootAndNotJustTheOwner
// below holds it to that.
func TestGuardStorePathRefusesAStoreSomeoneElseCanReach(t *testing.T) {
	me := uint32(os.Getuid())
	tests := []struct {
		name string
		// setup returns the store path, the owner the guard must demand, and
		// the component whose state decides: the refusal has to name THAT one,
		// or the user is sent to fix the wrong directory.
		setup func(t *testing.T) (path string, owner uint32, decides string)
		want  string
	}{
		{
			// A store the user owns is a store anything running as them can
			// swap. On an Intel Mac /usr/local is exactly that, which is why
			// the store is not there.
			name: "owned by someone other than the owner it demands",
			setup: func(t *testing.T) (string, uint32, string) {
				dir := filepath.Join(t.TempDir(), "versions")
				mkdirMode(t, dir, 0o755)
				return dir, me + 1, dir // the test's own files are foreign to the guard
			},
			want: fmt.Sprintf("owned by uid %d", me),
		},
		{
			// Group-writable: on macOS a store under a directory owned by
			// root:admin with g+w is writable by every admin account.
			name: "group-writable",
			setup: func(t *testing.T) (string, uint32, string) {
				dir := filepath.Join(t.TempDir(), "versions")
				mkdirMode(t, dir, 0o770)
				return dir, me, dir
			},
			want: "writable by group or others",
		},
		{
			name: "world-writable",
			setup: func(t *testing.T) (string, uint32, string) {
				dir := filepath.Join(t.TempDir(), "versions")
				mkdirMode(t, dir, 0o707)
				return dir, me, dir
			},
			want: "writable by group or others",
		},
		{
			// The first run: the store does not exist yet, so what decides is
			// the deepest component that does. Judging only the leaf would let
			// every fresh install through, which is when it matters most.
			name: "an ancestor is writable and the store is not there yet",
			setup: func(t *testing.T) (string, uint32, string) {
				parent := filepath.Join(t.TempDir(), "db")
				mkdirMode(t, parent, 0o777)
				return filepath.Join(parent, "plug", "versions"), me, parent
			},
			want: "writable by group or others",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, owner, decides := tt.setup(t)
			msg := guardRefusal(t, func() { guardStoreOwnedBy(path, owner, decides) })
			if msg == "" {
				t.Fatalf("the guard accepted %s as the store for a root-run core", path)
			}
			if !strings.Contains(msg, tt.want) {
				t.Errorf("the refusal does not say %q, so the user cannot tell what to fix:\n%s", tt.want, msg)
			}
			if !strings.Contains(msg, decides) {
				t.Errorf("the refusal names no path, or not %s, the component that decided it:\n%s", decides, msg)
			}
			if !strings.Contains(msg, "sudo chown root:wheel") {
				t.Errorf("the refusal offers no way out:\n%s", msg)
			}
		})
	}
}

// And the other half: a directory owned by the demanded owner, writable by
// nobody else, is accepted. A guard that refused this would make plug
// unusable on a correctly installed machine.
func TestGuardStorePathAcceptsARootOnlyStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "versions")
	mkdirMode(t, dir, 0o755)
	if msg := guardRefusal(t, func() { guardStoreOwnedBy(dir, uint32(os.Getuid()), dir) }); msg != "" {
		t.Fatalf("a store owned by the demanded owner and writable by nobody else was refused:\n%s", msg)
	}
}

// The owner the production guard demands is root, not "whoever happens to own
// the store". Pinning it to the current user would satisfy every test above and
// hand the store straight back to the account plug is protecting it from.
func TestTheStoreGuardDemandsRootAndNotJustTheOwner(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: the test's own directories are root-owned, so there is nothing to tell apart")
	}
	dir := filepath.Join(t.TempDir(), "versions")
	mkdirMode(t, dir, 0o755)
	if msg := guardRefusal(t, func() { guardStorePath(dir) }); msg == "" {
		t.Fatalf("guardStorePath accepted %s, owned by uid %d: the store must belong to root", dir, os.Getuid())
	}
}

// mkdirMode creates dir with mode, whatever the umask says: the modes are the
// subject here, and 0o777 through a 022 umask would silently become 0o755.
func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}
