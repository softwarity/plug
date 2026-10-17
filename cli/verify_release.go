package main

import (
	"errors"
	"fmt"
	"os"
)

// verifyReleaseVerb is how an installer asks the plug it has just written
// whether those bytes carry the release signature, BEFORE it grants them
// privilege: setcap on Linux, setuid root on macOS, the SYSTEM service on
// Windows. `plug update` and every core fetch already go through
// admitPrivilegedBytes; the first install was the one path that handed
// privilege to bytes nothing had checked.
//
// Be exact about what this proves. The check runs the very bytes it checks,
// from an installer written by the same agent that served them, so an agent
// that lies on purpose lies here too: nothing in-band can stop it on a first
// install, and its core would still be refused by the launcher afterwards. What
// it stops is the honest failures that used to end in a privileged binary: an
// image built without the release key, bytes damaged on the way or on disk, a
// file in the image that is not the one the release signed. A damaged binary
// does not get to answer at all, and the installer stops.
//
// It verifies a driver the same way (label wintun-<arch>), the installer for
// Windows naming the DLL it fetched beside plug.exe.
//
//	plug verify-release <file> <label> <sig>
const verifyReleaseVerb = "verify-release"

func verifyRelease(args []string) int {
	if len(args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: plug %s <file> <label> <sig>\n", verifyReleaseVerb)
		return 2
	}
	file, label, sig := args[0], args[1], args[2]
	measured, err := fileSHA256(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plug: cannot read %s: %v\n", file, err)
		return 1
	}
	err = verifyCore(coreAttestation{sha256: measured, sig: sig}, label, measured)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUnsignedCore):
		fmt.Fprintf(os.Stderr, "plug: this agent serves %s without a release signature, so it is not installed.\n"+
			"      Its image was built without the release key: deploy a published softwarity/plug image\n", label)
	default:
		fmt.Fprintf(os.Stderr, "plug: %s does not carry the plug release signature, so it is not installed.\n"+
			"      It was not built by the plug release workflow, or it was altered on the way\n", label)
	}
	return 1
}
