//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Windows has no setuid: plug elevates per launch (UAC / a SYSTEM service), it
// never runs the child from an inherited root euid, so there is nothing to drop
// and no helper bit to preserve.

func applyPrivDrop(*exec.Cmd) {}

func chownToUser(string) {}

// guardUserPath used to be empty here, on the reasoning that Windows never runs
// plug from an inherited root euid. That is true of the LAUNCHER and false of the
// daemon: the SYSTEM service reads its cluster address, and the path of the
// private key it dials with, out of %ProgramData%\plug, a directory the installer
// deliberately makes writable by users. So a plain user could name any path and
// have SYSTEM open it and present whatever parsed as a key to a server of their
// choosing, plus learn from the error whether a path exists.
//
// What this closes, plainly: the paths that make that worth doing. A key must be
// a REGULAR FILE, which rules out a named pipe or a device path, the shapes that
// turn an open() into something other than a read. And it must not live under a
// system root, which rules out aiming SYSTEM at a file the caller could never
// read themselves, the only case where SYSTEM's privilege buys the attacker
// anything.
//
// What this does NOT close, and it is worth writing down rather than implying
// otherwise: a user can still name a file inside another user's profile. Refusing
// that needs the daemon to open the key AS the registering account, which means
// impersonation and a token it does not have today. Until then the daemon reads
// paths from a directory users can write, and this narrows the target rather than
// removing it. The unix side has no such gap: there guardUserPath compares the
// real owner, because the daemon keeps the user's real uid.
func guardUserPath(p string) {
	if p == "" {
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		fatal("plug: %s is not a usable path (%v)", p, err)
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return // absent is the caller's problem to report, not a privilege question
	}
	if why := keyPathRefusal(abs, fi.Mode(), systemRootsForKeyGuard()); why != "" {
		fatal("plug: refusing to read %s as a key: %s", abs, why)
	}
}
