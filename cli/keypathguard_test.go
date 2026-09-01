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
