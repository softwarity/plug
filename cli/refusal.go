package main

import (
	"errors"
	"fmt"
)

// The privilege guards used to end in fatal(), and that was right at a CLI
// entry point: the person typed the command, the refusal is the answer. It was
// wrong everywhere a guard runs on somebody's behalf. The shared datapath
// daemon (macOS) and the SYSTEM service (Windows) hold the tunnels of every
// session of every account on the machine; the MCP server holds an editor's
// session. A profile key that one user mispointed reached a guard through
// reconcile, and os.Exit took every other user's datapath down with it, before
// the resolver was restored.
//
// So each guard has two faces: the fatal one for entry points, and a value for
// the paths that must carry on. A refusedByGuard is that value, and it is a type
// rather than a bare error for one reason: the daemon re-dials three times a
// second, and a refusal that will not change until a file does must back off
// the way an agent's "key not authorized" does (applyDial), not become a log
// line three times a second for the rest of the day.

type refusedByGuard struct{ msg string }

func (r *refusedByGuard) Error() string { return r.msg }

// refuse builds a refusedByGuard from a format, the way fatal would have printed
// it.
func refuse(format string, a ...any) error { return &refusedByGuard{msg: fmt.Sprintf(format, a...)} }

// isLocalRefusal reports whether err is a guard's refusal (either shape: the
// structured ownership walk, or a worded one), which is to say a condition on
// THIS machine that retrying will not change.
func isLocalRefusal(err error) bool {
	var g *refusedByGuard
	var f *foreignPath
	return errors.As(err, &g) || errors.As(err, &f)
}

// foreignPath is the refusal pathOwnedBy returns: the path that was asked
// about, the existing component it landed on, and the two uids that disagree.
// Structured rather than a string so a caller with a different privilege to
// explain (a mount, say) can word its own refusal from the same facts.
type foreignPath struct {
	path, landed string
	owner, uid   int
}

func (e *foreignPath) Error() string {
	return fmt.Sprintf("refusing to write %s as root: it resolves to %s, owned by uid %d, not by you (uid %d).\n"+
		"      plug runs setuid so it never has to ask for a password again, and it will not use that\n"+
		"      privilege to touch a file outside your own tree. Check $HOME and any symlink under it.",
		e.path, e.landed, e.owner, e.uid)
}
