//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

// Where a --mount may land, and why that is a privilege question.
//
// The mount is made with a privilege the person does not hold: euid 0 on macOS
// (the setuid launcher creates the directory and chowns it), CAP_SYS_ADMIN on
// Linux (mount(2) in the HOST namespace, over whatever directory was named).
// parseMount only asked for an absolute path, so the same user could have root
// create a directory of theirs under any root-owned one, or cover a system
// directory with a volume they control through their own agent. The rule in
// privdrop_unix.go applies here as it does to every other privileged write:
// plug only acts where the user could have acted unprivileged, which is a path
// whose deepest existing component they own.
//
// The automatic mounts are unaffected: they go under a session directory plug
// creates and hands to the user (startMounts), so that component is theirs.
// An explicit path under the home, or under anything else the person owns,
// passes. A path under /tmp does not when /tmp is where the walk stops, and
// the message names the alternative rather than leaving a bare refusal.

// ensureMountpoint creates the directory as the user and refuses a path that
// is already something else (a file, a busy mount) or that is not the user's
// to mount over. The ownership question is asked FIRST: the creation below
// runs with privilege, and would otherwise be the very write being refused.
func ensureMountpoint(path string) error {
	if err := mountpointOwnedBy(path, realUID()); err != nil {
		return err
	}
	if mountedAt(path) {
		return fmt.Errorf("%s is already a mountpoint (a previous session's? plug doctor --fix)", path)
	}
	st, err := os.Stat(path)
	if err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	chownToUser(path)
	return nil
}

// mountpointOwnedBy is the decision, with the uid passed in so a test can hand
// it a uid that is not its own and watch it refuse the test's own directories:
// the only way an unprivileged process can exercise the comparison, and the
// same comparison the launcher makes for real. nil means the mount may go there.
func mountpointOwnedBy(path string, uid int) error {
	err := pathOwnedBy(path, uid)
	var fp *foreignPath
	if !errors.As(err, &fp) {
		return err
	}
	return fmt.Errorf("refusing to mount at %s: it resolves to %s, owned by uid %d, not by you (uid %d).\n"+
		"      plug mounts with a privilege you do not hold, and only where you could have created\n"+
		"      the directory yourself. Mount under your home (--mount <volume>:$HOME/...), or leave\n"+
		"      the path out and let plug place the volume under a session directory of yours",
		fp.path, fp.landed, fp.owner, fp.uid)
}
