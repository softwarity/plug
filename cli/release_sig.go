package main

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// --- Release signatures over the core binary ---------------------------------
//
// The launcher runs the core WITH THE PRIVILEGE IT HOLDS: root on macOS (setuid),
// ambient CAP_SYS_ADMIN on Linux. Until this file existed, the only thing vouching
// for those bytes was a SHA-256 the same party had just announced, so whoever
// chose the agent chose what root executed. And the agent is chosen by the caller:
// `plug -H <host> --port <port>`, or a `host =` line in a profile the user owns.
// Any unprivileged code running as the user could therefore stand up a fake agent
// on 127.0.0.1, answer three questions, and have its binary executed as root.
//
// A digest cannot fix that: it binds the bytes to a claim, not to an author. Only
// a signature the attacker cannot produce does, which means a key that is NOT in
// anything the attacker can write. Nothing under the user's home qualifies, since
// the attacker runs as the user and can rewrite it, known_hosts included. So the
// trust anchor is compiled in, right here, and the private half never leaves the
// release workflow.

// The parser takes one key per line, but the POLICY is one key at a time, and
// the file says why. Trusting several keys would make recovery from a lost key
// cheaper and recovery from a STOLEN one worse: the stolen key keeps working
// until somebody deliberately removes its line, whereas with a single key,
// replacing it and revoking it are the same act. Between a failure mode that
// costs a reinstall and one that leaves an attacker signing, the reinstall wins.
//
// The plural stays in the parser because it costs nothing and keeps a staged
// rotation possible if it is ever wanted. Comments and blank lines are allowed.
//
// errUnsignedCore says the agent is simply too old to sign, which is a different
// thing from a signature that does not check out, and the two deserve different
// answers. An absent signature is age: refuse to INSTALL those bytes, but do not
// take the process down over it, because whatever else the caller was doing may
// well have succeeded. A signature that fails to verify is tampering, and there
// is nothing to carry on with.
var errUnsignedCore = errors.New("this agent is too old to sign what it serves, and plug will not run an\n" +
	"      unsigned core with root privilege. Redeploy the softwarity/plug image on this\n" +
	"      cluster; its agent must be recent enough to answer 'sig=' to the digest verb")

//go:embed keys/release_ed25519.pub
var releasePubKeysRaw string

// coreAttestation is what an agent says about the binary it serves. The digest
// verb answers key=value lines precisely so this could grow a sig without
// breaking a parser written before it existed.
type coreAttestation struct {
	sha256 string
	sig    string // base64 ed25519 over coreStatement; empty from an agent built before signing
}

// coreStatement is what gets signed: the platform and the hash, and deliberately
// NOT the version.
//
// Binding the version looked like free defence in depth and is not. It buys
// almost nothing, because the version is announced by the very party being
// checked: an attacker replaying a genuinely signed old binary just announces
// that binary's version and the binding is satisfied. And it costs a great deal,
// because an EMBEDDER sets Config.Version to its own identity while serving plug
// binaries signed by this pipeline: the signature would cover plug's version, the
// check would use the embedder's, and every launch would be refused. (That trap
// was written up in a design note this repository no longer carries; the reason
// is above, which is where it belongs.)
//
// This format is a wire contract from the first release that ships it. Changing
// it later breaks every CLI already carrying the old one, so it is worth being
// right about now rather than sorry about in a year.
func coreStatement(osArch, sha256hex string) []byte {
	return []byte("plug-core-v1\n" + osArch + "\n" + sha256hex + "\n")
}

func releasePubKeys() ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for n, line := range strings.Split(releasePubKeysRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("the built-in release key on line %d is unreadable (%v)", n+1, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("the built-in release key on line %d is %d bytes, expected %d", n+1, len(raw), ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("this plug was built with no release key, so it cannot verify a core")
	}
	return keys, nil
}

// verifyCore decides whether these bytes may be executed with privilege. It is
// given the digest the CLI computed ITSELF from the bytes on disk, never the one
// the agent announced: the signature vouches for a hash, and that hash has to be
// the one we measured or the chain proves nothing.
func verifyCore(att coreAttestation, osArch, measured string) error {
	if att.sha256 != measured {
		return fmt.Errorf("the core does not hash to what the agent announced")
	}
	// No grace period, and no version below which unsigned is tolerated. A window
	// would have to be closed by something, and every value this code could read
	// from the agent is chosen by the party being checked: an agent that wanted
	// the tolerant branch would simply claim whatever bought it.
	//
	// It costs nothing to anyone who has not moved. An old CLI against an old
	// agent never reaches this function, so an untouched pair keeps working
	// exactly as before, bugs and all. The only pair this refuses is one where
	// half has already been updated, and there the answer is to update the other
	// half: plug is a developer tool, and redeploying its agent is one command.
	if att.sig == "" {
		return errUnsignedCore
	}
	keys, err := releasePubKeys()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(att.sig))
	if err != nil {
		return fmt.Errorf("the agent answered a malformed signature (%v)", err)
	}
	stmt := coreStatement(osArch, measured)
	ok := false
	for _, key := range keys {
		if ed25519.Verify(key, stmt, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("the core's signature does not check out against the release key built into this plug.\n" +
			"      plug runs the core with root privilege, so it will not execute bytes it cannot attribute\n" +
			"      to the plug release workflow.\n" +
			"      If the cluster is yours and its agent is genuine, this plug predates a replacement of the\n" +
			"      release key, and the fix is to reinstall it from the cluster:\n" +
			"        ssh -p <port> get@<host> install | sh\n" +
			"      (Git Bash on Windows: ssh -n -p <port> get@<host> install-windows | bash -s -- <host> <port>)")
	}
	return nil
}
