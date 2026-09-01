package main

import "testing"

// A REAL reply, captured from a softwarity/plug image built by agent/Dockerfile
// with the release key mounted, for linux-arm64. The launcher's parser and its
// verifier are run against it with the PRODUCTION key, the one embedded from
// keys/release_ed25519.pub.
//
// Why a captured fixture and not a mock: the signer runs inside the image build,
// the verifier runs on the user's machine, and between them sit a shell script, a
// key=value protocol and one statement format written out in two separate files.
// Every other test in this package can pass while those two halves have quietly
// stopped meeting. This one cannot, and it has already earned its keep once: it
// is what failed the moment the statement format changed.
const realAgentDigestReply = `
sha256=fdc43cd77f4ae24a2fcbbd47721ea98eba7686f6358db08e1543d6e7469c96ab
sig=Cgnj8U/x30m0PSMGpQwAfa/WrCcnY/C0GTxfZohrUGhWbcnmEZeuiTV8dSP6XhLYuohUEdEcNj1xMKlAGBgWAg==
`

func TestARealAgentReplyVerifiesAgainstTheShippedKey(t *testing.T) {
	att, err := parseAttestation(realAgentDigestReply, "linux-arm64")
	if err != nil {
		t.Fatalf("the launcher cannot parse what the agent actually answers: %v", err)
	}
	if att.sig == "" {
		t.Fatal("the agent served a signature and the parser dropped it: every core would look unsigned")
	}
	if err := verifyCore(att, "linux-arm64", att.sha256); err != nil {
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
	if err := verifyCore(att, "darwin-arm64", att.sha256); err == nil {
		t.Fatal("the linux-arm64 signature was accepted for darwin-arm64")
	}
}
