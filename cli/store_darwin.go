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
func guardStorePath(path string) {
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err == nil {
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				return
			}
			if st.Uid != 0 {
				fatal(storeOwnershipMsg(p, fmt.Sprintf("it is owned by uid %d, not root", st.Uid)))
			}
			if fi.Mode().Perm()&0o022 != 0 {
				fatal(storeOwnershipMsg(p, fmt.Sprintf("it is writable by group or others (%v)", fi.Mode().Perm())))
			}
			return // the deepest existing component is sound; what we create below it is ours
		}
		if filepath.Dir(p) == p {
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
