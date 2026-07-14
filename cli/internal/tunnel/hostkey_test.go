package tunnel

import (
	"crypto/ed25519"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// A regenerated agent host key (the agent runs ssh-keygen -A every start) must
// NOT block the connection — the client re-pins it and carries on. This locks
// that behaviour: the old code returned an error and made the user hand-edit
// known_hosts after every restart.
func TestTofuRepinsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	addr := "cluster.example:2222"

	key1 := genHostKey(t)
	key2 := genHostKey(t)
	cb := tofuHostKey(path, addr, func(string, ...any) {})

	// First sight pins key1.
	if err := cb(addr, dummyAddr{}, key1); err != nil {
		t.Fatalf("first pin: %v", err)
	}
	if got := pinnedKeys(t, path, addr); len(got) != 1 {
		t.Fatalf("want 1 pinned line for %s, got %d", addr, len(got))
	}

	// Same key again → no-op, no error.
	if err := cb(addr, dummyAddr{}, key1); err != nil {
		t.Fatalf("re-see same key: %v", err)
	}

	// Changed key → re-pinned, NOT rejected.
	if err := cb(addr, dummyAddr{}, key2); err != nil {
		t.Fatalf("changed key should re-pin, not error: %v", err)
	}
	lines := pinnedKeys(t, path, addr)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 pinned line after re-pin (no stale dup), got %d: %v", len(lines), lines)
	}
	wantEnc := key2.Type() + " " + sshMarshal(key2)
	if lines[0] != wantEnc {
		t.Fatalf("pinned key not updated to the new one")
	}
}

func pinnedKeys(t *testing.T, path, addr string) []string {
	t.Helper()
	data, _ := os.ReadFile(path)
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(f) == 2 && f[0] == addr {
			out = append(out, f[1])
		}
	}
	return out
}

func sshMarshal(k ssh.PublicKey) string {
	// mirrors tofuHostKey's encoding
	return base64.StdEncoding.EncodeToString(k.Marshal())
}

func genHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

type dummyAddr struct{}

func (dummyAddr) Network() string { return "tcp" }
func (dummyAddr) String() string  { return "cluster.example:2222" }

var _ net.Addr = dummyAddr{}
