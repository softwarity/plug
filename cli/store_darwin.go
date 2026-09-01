package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Where the cached cores live on macOS, and why it is not under $HOME.
//
// Here the launcher is setuid root and does NOT drop privilege before running
// the core — applyPrivDrop exists for YOUR command, one level further down, not
// for this. The core therefore runs as root. A store the user can write is a
// store the user can hand root a different binary from, and "the user" includes
// anything running as them.
//
// /var/db rather than /usr/local: on an Intel Mac with Homebrew, /usr/local is
// routinely owned by the human, which would put the store back within reach
// through its own parent. /var/db is root:wheel on every macOS and belongs to no
// package manager. guardStorePath checks that rather than trusting this note.
var versionsDir = func() string { return "/var/db/plug/versions" }

// legacyVersionsDir is where they used to live. prune, doctor and uninstall
// empty it: a cached core is disposable — re-downloaded on demand and verified
// on every launch — so there is nothing to migrate, only something not to leave
// behind for a `prune` that no longer looks there.
func legacyVersionsDir() string { return filepath.Join(plugDir(), "versions") }

// storeIsSystem: the store belongs to root, so it must NOT be handed back to
// the user the way the old one was.
func storeIsSystem() bool { return true }

// guardStorePath refuses to write the store unless every existing component of
// its path is owned by root and writable by root alone.
//
// This is the check the whole arrangement rests on, so it is made rather than
// assumed: a parent someone else can write is a parent someone else can replace
// with a symlink, and the store would be back in reach without a single
// permission on the store itself looking wrong.
// A var, and only so a test can stand in for it: the check itself is fatal, so
// nothing can assert what ensureVersion does around it without a seam. Never
// reassigned outside tests.
var guardStorePath = func(path string) { guardStoreOwnedBy(path, 0, "") }

// guardStoreOwnedBy is the check itself, with the owner it demands passed in
// instead of root being written into the comparison.
//
// Split out for one reason: no test running as a normal user can create a
// root-owned directory, so with 0 baked in the two refusals could only ever be
// pointed at the host machine's own /var/db, which is to say never exercised at
// all. That is how they stayed at zero hits while the test file claimed they
// were asserted. With the owner as an argument the whole walk runs on a
// t.TempDir(), where a test can hand it a directory that is group-writable,
// world-writable or owned by a stranger and watch it refuse each one. The
// refusal still says "not root", because root is the only owner the one
// production caller ever asks for.
// stopAt bounds the walk, and exists for one reason: the walk goes to the
// filesystem root, so no fixture under a temp directory can ever have a coherent
// chain of ancestors, and a guard that decides what root executes must be
// testable on something other than the machine it happens to run on. Production
// passes "" and walks the whole way.
func guardStoreOwnedBy(path string, owner uint32, stopAt string) {
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err == nil {
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				return
			}
			if st.Uid != owner {
				guardFatal("%s", storeOwnershipMsg(p, fmt.Sprintf("it is owned by uid %d, not root", st.Uid)))
			}
			if fi.Mode().Perm()&0o022 != 0 {
				guardFatal("%s", storeOwnershipMsg(p, fmt.Sprintf("it is writable by group or others (%v)", fi.Mode().Perm())))
			}
			// EVERY existing component, all the way up, not just the deepest one.
			// Stopping there rested on "this component is sound, so what we put
			// under it is ours", and that only holds if its ANCESTORS are sound
			// too. With /var/db/plug world-writable and /var/db/plug/versions
			// underneath it root-owned and tidy, the old walk looked at versions,
			// approved, and never looked up. Anyone able to write the parent could
			// then move that directory aside and put their own in its place, and
			// what root executes on the next launch comes out of it. The extra
			// cost is four stat calls on a path five components deep.
		}
		if p == stopAt || filepath.Dir(p) == p {
			return
		}
	}
}

func storeOwnershipMsg(p, why string) string {
	return fmt.Sprintf("refusing to use %s as the core store: %s.\n"+
		"      plug runs the cached core with root privilege, so the directory holding it\n"+
		"      must not be writable by anyone else. Fix it with:\n"+
		"        sudo chown root:wheel %s && sudo chmod 755 %s", p, why, p, p)
}
