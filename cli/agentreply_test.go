package main

import "testing"

// A REAL reply, captured from a softwarity/plug image built by agent/Dockerfile
// with the release key mounted, for linux-arm64 at version 2.13.0. The launcher's
// parser and its verifier are run against it with the PRODUCTION key, the one
// embedded from keys/release_ed25519.pub.
//
// Why a captured fixture and not a mock: the signer runs inside the image build,
// the verifier runs on the user's machine, and between them sit a shell script, a
// key=value protocol and one statement format written out in two separate files.
// Every other test in this package can pass while those two halves have quietly
// stopped meeting. This one cannot.
const realAgentDigestReply = `
sha256=bb27d57f731edde772ee8378be8adb69a26e469791f1bad21ae60a0e7bcc1808
sig=JiorGBD/dDtPaxcxNtBtEkLuSTJf2sTR9wObeznoag62EwJ4vgmN3iYTbw9mLW7yyFchXWlfknK1FWLnw6tQAA==
`

func TestARealAgentReplyVerifiesAgainstTheShippedKey(t *testing.T) {
	att, err := parseAttestation(realAgentDigestReply, "linux-arm64")
	if err != nil {
		t.Fatalf("the launcher cannot parse what the agent actually answers: %v", err)
	}
	if att.sig == "" {
		t.Fatal("the agent served a signature and the parser dropped it: every core would look unsigned")
	}
	if err := verifyCore(att, "linux-arm64", "2.13.0", att.sha256); err != nil {
		t.Fatalf("the launcher refuses a core signed by the release workflow: %v", err)
	}
}

// And the same reply must NOT verify as something else, which is what proves the
// test above reads the signature rather than merely finding one.
func TestTheRealReplyDoesNotVerifyForAnotherPlatform(t *testing.T) {
	att, err := parseAttestation(realAgentDigestReply, "linux-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCore(att, "darwin-arm64", "2.13.0", att.sha256); err == nil {
		t.Fatal("the linux-arm64 signature was accepted for darwin-arm64")
	}
}
