package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The signer and the verifier are two programs that must agree on one string,
// byte for byte, and they live in different files. If they ever drift, nothing
// fails loudly: signatures are simply produced that never verify, and the
// launcher refuses every core after the cutover, or worse, before it, warns and
// carries on as though nothing were wrong. So run the real signer and verify its
// real output with the real verifier.
func TestTheSignerAndTheVerifierAgree(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to run the signer with")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One per shape the release actually publishes, including the .exe, whose
	// suffix has to be stripped to recover the os-arch or the statement differs.
	bodies := map[string][]byte{
		"plug-darwin-arm64":      []byte("a darwin binary"),
		"plug-linux-amd64":       []byte("a linux binary"),
		"plug-windows-amd64.exe": []byte("a windows binary"),
	}
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(binDir, name), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const version = "2.13.0+abc1234"

	out, err := exec.Command("go", "run", "./cmd/plug-sign", keyFile, binDir, version).CombinedOutput()
	if err != nil {
		t.Fatalf("the signer failed: %v\n%s", err, out)
	}

	saved := releasePubKeysRaw
	defer func() { releasePubKeysRaw = saved }()
	releasePubKeysRaw = base64.StdEncoding.EncodeToString(pub)

	for name, body := range bodies {
		sig, err := os.ReadFile(filepath.Join(binDir, name+".sig"))
		if err != nil {
			t.Fatalf("the signer wrote no signature for %s: %v", name, err)
		}
		osArch := strings.TrimSuffix(strings.TrimPrefix(name, "plug-"), ".exe")
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		att := coreAttestation{sha256: sum, sig: strings.TrimSpace(string(sig))}
		if err := verifyCore(att, osArch, version, sum); err != nil {
			t.Errorf("the launcher refuses what the release workflow signed for %s: %v", name, err)
		}
	}
}

// And it must refuse to produce a release that carries no signature at all,
// because that failure is invisible until the cutover date, by which point the
// image is published.
func TestTheSignerRefusesAnEmptyBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to run the signer with")
	}
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyFile := filepath.Join(dir, "key")
	os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600)
	empty := filepath.Join(dir, "bin")
	os.MkdirAll(empty, 0o755)

	out, err := exec.Command("go", "run", "./cmd/plug-sign", keyFile, empty, "2.13.0").CombinedOutput()
	if err == nil {
		t.Fatalf("the signer accepted a build with no binaries in it:\n%s", out)
	}
	if !strings.Contains(string(out), "would ship unsigned") {
		t.Errorf("the signer failed but did not say why: %s", out)
	}
}
