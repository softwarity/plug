package main

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
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
//go:embed keys/release_ed25519.pub
var releasePubKeysRaw string

// signedFrom is the date this becomes mandatory. Before it, an unsigned core is
// accepted and says so; after, it is refused.
//
// Why a DATE and not a version. The obvious rule, "accept unsigned from an agent
// older than the signing release", is worthless: the fake agent announces its own
// version, so it would simply claim to be old. Every value the check could read
// from the agent is chosen by the party being checked. A date is the one input
// that is not, and it lets a fleet migrate agents before its CLIs cut over rather
// than breaking everyone the day a CLI updates.
const signedFrom = "2026-12-01"

// coreAttestation is what an agent says about the binary it serves. The digest
// verb answers key=value lines precisely so this could grow a sig without
// breaking a parser written before it existed.
type coreAttestation struct {
	sha256 string
	sig    string // base64 ed25519 over coreStatement; empty from an agent built before signing
}

// coreStatement is what gets signed. Not the bare hash: the platform and the
// version are bound in too, so a signature genuinely issued for the linux binary
// cannot be replayed over the darwin one, nor an old release's signature over a
// new version's bytes.
func coreStatement(osArch, version, sha256hex string) []byte {
	return []byte("plug-core-v1\n" + osArch + "\n" + strings.TrimPrefix(version, "v") + "\n" + sha256hex + "\n")
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
func verifyCore(att coreAttestation, osArch, version, measured string) error {
	if att.sha256 != measured {
		return fmt.Errorf("the core does not hash to what the agent announced")
	}
	if att.sig == "" {
		if now().Before(signedFromDate()) {
			info("WARNING this agent serves an UNSIGNED core, so plug is trusting the cluster it was pointed at.\n"+
				"      Redeploy the softwarity/plug image before %s: after that date plug refuses to run an unsigned core with privilege.", signedFrom)
			return nil
		}
		return fmt.Errorf("this agent serves an UNSIGNED core, and plug will not run one with privilege.\n"+
			"      Signatures became mandatory on %s. Redeploy the softwarity/plug image on this cluster;\n"+
			"      the agent must be recent enough to answer 'sig=' to the digest verb", signedFrom)
	}
	keys, err := releasePubKeys()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(att.sig))
	if err != nil {
		return fmt.Errorf("the agent answered a malformed signature (%v)", err)
	}
	stmt := coreStatement(osArch, version, measured)
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

// now is a var so a test can stand on either side of the deadline. The cutover
// is the whole point of the design, and a date that only the calendar can reach
// is a date nobody ever tests.
var now = time.Now

func signedFromDate() time.Time {
	d, err := time.Parse("2006-01-02", signedFrom)
	if err != nil {
		return time.Time{} // an unparsable constant means mandatory now, never never
	}
	return d
}
