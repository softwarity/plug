package main

import (
	"os"
	"strings"
	"testing"
)

// On Windows the daemon runs as SYSTEM and reads the path of the key it dials
// with out of %ProgramData%\plug, which the installer deliberately makes writable
// by users. guardUserPath there was an empty function, so a plain user could name
// any path at all and have the most privileged account on the machine open it.
//
// These run on every platform on purpose. The rule only bites on Windows, but a
// rule exercised on one CI leg and on nobody's machine is a rule nobody will
// notice breaking.
func TestASystemServiceWillNotOpenJustAnyKeyPath(t *testing.T) {
	roots := []string{`C:\Windows`, `C:\ProgramData`, `C:\Program Files`}

	// A pipe or a device where a key file should be: opening it is not a read.
	if why := keyPathRefusal(`C:\Users\bob\.plug\keys\dev`, os.ModeNamedPipe, roots); why == "" {
		t.Error("a named pipe was accepted as a key file")
	}
	if why := keyPathRefusal(`C:\Users\bob\.plug\keys\dev`, os.ModeDevice, roots); why == "" {
		t.Error("a device was accepted as a key file")
	}
	if why := keyPathRefusal(`C:\Users\bob\.plug\keys\dev`, os.ModeDir, roots); why == "" {
		t.Error("a directory was accepted as a key file")
	}

	// A file the caller could never read themselves. This is the only case where
	// SYSTEM's privilege buys the attacker anything at all.
	for _, p := range []string{
		`C:\Windows\System32\config\SAM`,
		`C:\windows\system32\drivers\etc\hosts`, // casing must not matter
		`C:\ProgramData\plug\anything`,
		`C:\Program Files\something\key`,
	} {
		if why := keyPathRefusal(p, 0, roots); why == "" {
			t.Errorf("%s was accepted: SYSTEM would open a file its caller cannot", p)
		} else if !strings.Contains(why, "system directory") {
			t.Errorf("%s was refused for the wrong reason: %s", p, why)
		}
	}
}

// And it must not refuse a real key, or the daemon dials with the built-in
// identity instead of the user's and every personal key silently stops working.
func TestARealKeyPathIsStillAccepted(t *testing.T) {
	roots := []string{`C:\Windows`, `C:\ProgramData`, `C:\Program Files`}
	for _, p := range []string{
		`C:\Users\bob\.plug\keys\dev`,
		`D:\work\keys\id_ed25519`,
		`C:\Users\bob\Documents\my key`,
	} {
		if why := keyPathRefusal(p, 0, roots); why != "" {
			t.Errorf("%s is a legitimate key path and was refused: %s", p, why)
		}
	}
}

// A prefix match on the raw string refuses the wrong thing while looking right:
// C:\WindowsApps is not inside C:\Windows.
func TestASiblingOfASystemDirectoryIsNotInsideIt(t *testing.T) {
	roots := []string{`C:\Windows`}
	for _, p := range []string{`C:\WindowsApps\keys\dev`, `C:\Windows-old\keys\dev`} {
		if why := keyPathRefusal(p, 0, roots); why != "" {
			t.Errorf("%s only shares a prefix with a system root, it is not under it: %s", p, why)
		}
	}
	if keyPathRefusal(`C:\Windows`, 0, roots) == "" {
		t.Error("the system root itself was accepted")
	}
}

// An empty root must never be read as "everything is under it", which would
// refuse every key on a machine where the variable is unset.
func TestAnEmptyRootRefusesNothing(t *testing.T) {
	if why := keyPathRefusal(`C:\Users\bob\.plug\keys\dev`, 0, []string{"", "   "}); why != "" {
		t.Errorf("an unset system root cut off a legitimate key: %s", why)
	}
}

// The SYSTEM service on Windows takes a key path out of a directory plain users
// can write. Narrowing it to regular files outside system directories left the
// case that matters: a user naming a file inside ANOTHER user's profile, and
// having the machine account open it for them.
//
// The answer cannot come from anything the client wrote, since the same user
// wrote it. It comes from who OWNS the marker, which Windows records and a caller
// cannot claim, and from that account's profile directory, which lives in a
// registry key only administrators can write.
func TestAKeyOutsideItsOwnersProfileIsRefused(t *testing.T) {
	const bob = `C:\Users\bob`
	for _, p := range []string{
		`C:\Users\alice\.plug\keys\dev`, // the case this exists for
		`C:\Users\alice\id_ed25519`,
		`D:\somewhere\else\key`,
		`C:\Users\bob2\.plug\keys\dev`, // a sibling, not a child
		`C:\Users\bobby\.plug\keys\dev`,
	} {
		if !keyOutsideOwnersProfile(p, bob) {
			t.Errorf("%s was accepted for a client registered by %s; the service would read one "+
				"account's file on another's say-so", p, bob)
		}
	}
}

func TestAKeyInsideItsOwnersProfileIsAccepted(t *testing.T) {
	const bob = `C:\Users\bob`
	for _, p := range []string{
		`C:\Users\bob\.plug\keys\dev`,
		`C:\Users\bob\Documents\keys\id_ed25519`,
		`c:\users\bob\.plug\keys\dev`,  // casing must not matter
		`C:/Users/bob/.plug/keys/dev`,  // nor the separator Windows also accepts
		`C:\Users\bob\\.plug\keys\dev`, // nor a doubled one
	} {
		if keyOutsideOwnersProfile(p, bob) {
			t.Errorf("%s is inside %s and was refused, which would stop that user's own key from "+
				"being used at all", p, bob)
		}
	}
}

// Unknown is not outside. Every lookup behind this can fail on a machine that is
// perfectly fine, and refusing then would turn a service that reads one file too
// freely into a service that reads none.
func TestAnUnknownProfileRefusesNothing(t *testing.T) {
	if keyOutsideOwnersProfile(`C:\Users\bob\.plug\keys\dev`, "") {
		t.Error("an unknown owner profile was treated as a refusal")
	}
	if keyOutsideOwnersProfile("", `C:\Users\bob`) {
		t.Error("an empty key path was treated as a refusal")
	}
}
