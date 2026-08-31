package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// The core is executed with root privilege on macOS and CAP_SYS_ADMIN on Linux,
// and the agent that serves it is chosen by whoever runs plug. Everything below
// is one question: can a party who is not holding the release private key get
// bytes past this function?

func testKey(t *testing.T) (ed25519.PrivateKey, func()) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	saved := releasePubKeysRaw
	releasePubKeysRaw = base64.StdEncoding.EncodeToString(pub)
	return priv, func() { releasePubKeysRaw = saved }
}

func signed(priv ed25519.PrivateKey, osArch, version, sum string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, coreStatement(osArch, version, sum)))
}

const aSum = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"

func TestASignedCoreIsAccepted(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.13.0", aSum)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err != nil {
		t.Fatalf("a core signed by the release key was refused: %v", err)
	}
}

// The attack this whole file exists for: anybody can stand up an agent, and an
// agent can say anything. What it cannot do is sign.
func TestACoreSignedByAnybodyElseIsRefused(t *testing.T) {
	_, restore := testKey(t)
	defer restore()
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	att := coreAttestation{sha256: aSum, sig: signed(attacker, "darwin-arm64", "2.13.0", aSum)}
	err := verifyCore(att, "darwin-arm64", "2.13.0", aSum)
	if err == nil {
		t.Fatal("a core signed by a key plug does not know was accepted: root would run a stranger's binary")
	}
	if !strings.Contains(err.Error(), "does not check out") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The statement binds the platform, so a signature genuinely issued by the
// release workflow for one target cannot be lifted onto another.
func TestASignatureDoesNotTravelBetweenPlatforms(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	att := coreAttestation{sha256: aSum, sig: signed(priv, "linux-amd64", "2.13.0", aSum)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("a signature issued for linux-amd64 was accepted for darwin-arm64")
	}
}

// And it binds the version, so an old release's signature cannot be presented
// over a newer version's bytes.
func TestASignatureDoesNotTravelBetweenVersions(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.5.0", aSum)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("a signature issued for 2.5.0 was accepted for 2.13.0")
	}
}

// The signature vouches for a hash. If the hash it vouches for is not the one we
// measured from the bytes on disk, the chain is broken and the signature is
// decoration.
func TestTheSignatureMustCoverTheBytesWeMeasured(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	other := strings.Repeat("b", 64)
	att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.13.0", other)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("a signature over different bytes than the ones measured was accepted")
	}
}

func TestAnAnnouncedDigestThatIsNotTheMeasuredOneIsRefused(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	measured := strings.Repeat("c", 64)
	att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.13.0", measured)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", measured); err == nil {
		t.Fatal("the agent announced one digest and the bytes hashed to another, and it passed")
	}
}

func TestAMalformedSignatureIsRefusedRatherThanIgnored(t *testing.T) {
	_, restore := testKey(t)
	defer restore()
	att := coreAttestation{sha256: aSum, sig: "not base64 at all !!"}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("a signature that is not even base64 was treated as valid")
	}
}

// The cutover. Both sides of it, because a deadline nobody tests is a deadline
// that turns out to be wired backwards on the day it fires.
func TestAnUnsignedCoreIsToleratedBeforeTheCutover(t *testing.T) {
	saved := now
	defer func() { now = saved }()
	now = func() time.Time { return signedFromDate().Add(-24 * time.Hour) }

	if err := verifyCore(coreAttestation{sha256: aSum}, "darwin-arm64", "2.13.0", aSum); err != nil {
		t.Fatalf("an unsigned core was refused before the cutover, which strands every cluster not yet redeployed: %v", err)
	}
}

func TestAnUnsignedCoreIsRefusedAfterTheCutover(t *testing.T) {
	saved := now
	defer func() { now = saved }()
	now = func() time.Time { return signedFromDate().Add(24 * time.Hour) }

	err := verifyCore(coreAttestation{sha256: aSum}, "darwin-arm64", "2.13.0", aSum)
	if err == nil {
		t.Fatal("after the cutover an unsigned core was still accepted: the hole is open")
	}
	if !strings.Contains(err.Error(), "UNSIGNED") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
}

// The key shipped in the binary must be a usable ed25519 key. A truncated or
// mis-encoded file would make every signature fail closed, which is safe but
// would strand everyone: worth catching at build time rather than in the field.
func TestTheBuiltInReleaseKeyIsWellFormed(t *testing.T) {
	keys, err := releasePubKeys()
	if err != nil {
		t.Fatalf("the release keys compiled into plug are unusable: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("plug ships no release key")
	}
}

// plug ships one key, but the parser takes a list, and a staged rotation is the
// only thing that list would ever be for. Kept tested so the capability is real
// if it is ever wanted, rather than a comment claiming something untried.
func TestACoreSignedWithEitherTrustedKeyIsAccepted(t *testing.T) {
	oldPub, oldPriv, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)

	saved := releasePubKeysRaw
	defer func() { releasePubKeysRaw = saved }()
	releasePubKeysRaw = "# retired 2027-01-01, kept so cores signed before then still run\n" +
		base64.StdEncoding.EncodeToString(oldPub) + "\n\n" +
		base64.StdEncoding.EncodeToString(newPub) + "\n"

	for name, priv := range map[string]ed25519.PrivateKey{"the old key": oldPriv, "the new key": newPriv} {
		att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.13.0", aSum)}
		if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err != nil {
			t.Errorf("a core signed with %s was refused during the rotation: %v", name, err)
		}
	}

	// And a third party is still refused, which is the point of the whole file.
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	att := coreAttestation{sha256: aSum, sig: signed(attacker, "darwin-arm64", "2.13.0", aSum)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("widening the key list let an untrusted key through")
	}
}

// A build with no key at all must fail closed, not open. An embed that silently
// produced an empty string would otherwise turn every verification into a pass.
func TestABuildWithNoReleaseKeyRefusesEverything(t *testing.T) {
	saved := releasePubKeysRaw
	defer func() { releasePubKeysRaw = saved }()
	releasePubKeysRaw = "# every key retired, none issued\n"

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	att := coreAttestation{sha256: aSum, sig: signed(priv, "darwin-arm64", "2.13.0", aSum)}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", aSum); err == nil {
		t.Fatal("a plug built with no release key accepted a signature: it verifies nothing and says nothing")
	}
}
