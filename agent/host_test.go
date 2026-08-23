package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeHost is what Meerkat will be: a key store it owns, an identity it keeps,
// and notifications it records. Nothing here is test scaffolding for its own
// sake - it is the shape of the real implementation, exercised.
type fakeHost struct {
	signer  ssh.Signer
	allowed map[string]string // fingerprint -> developer name

	mu       sync.Mutex
	served   []NameEvent
	unserved []string
}

func (h *fakeHost) HostKey() (ssh.Signer, error) { return h.signer, nil }

func (h *fakeHost) Verify(key ssh.PublicKey) (string, bool) {
	who, ok := h.allowed[ssh.FingerprintSHA256(key)]
	return who, ok
}

func (h *fakeHost) Served(e NameEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.served = append(h.served, e)
}

func (h *fakeHost) Unserved(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unserved = append(h.unserved, name)
}

// The point of phase 0: an embedder supplies a Host and gets a working agent,
// without the package deciding anything for it - no os.Exit, no file it must
// create, no identity it does not control.
func TestAnEmbedderDrivesTheAgentThroughTheInterface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	clientSigner, _ := ssh.NewSignerFromKey(priv)
	clientPub, _ := ssh.NewPublicKey(pub)

	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	host := &fakeHost{
		signer:  hostSigner,
		allowed: map[string]string{ssh.FingerprintSHA256(clientPub): "alice"},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Start(ctx, Config{
			Host: host,
			Addr: addr,
			// Meerkat re-execs itself with a hidden verb; a test just echoes.
			VerbCommand:     []string{"/bin/sh", "-c", `printf 'verb:%s' "$SSH_ORIGINAL_COMMAND"`},
			DownloadCommand: []string{"/bin/sh", "-c", `printf 'dl:%s' "$SSH_ORIGINAL_COMMAND"`},
			Logf:            func(string, ...any) {},
			// A gateway must keep running without orchestrator access: this is
			// the flag that makes a forgotten RBAC rule a degraded feature
			// instead of a process that refuses to boot.
			RequireOrchestrator: false,
		})
	}()

	// Wait for the listener rather than sleeping blind.
	var cl *ssh.Client
	for i := 0; i < 100; i++ {
		cl, err = ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            tunnelUser,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
			HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()),
			Timeout:         2 * time.Second,
		})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the embedder's agent never accepted a connection: %v", err)
	}
	defer cl.Close()

	// The identity came from the Host, so pinning it is meaningful: a caller
	// that keeps its key in a vault gets a server clients can actually verify.
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("serve-name x 1:2 takeover")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := string(out); got != "verb:serve-name x 1:2 takeover" {
		t.Errorf("the verb command must receive the request, got %q", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start must return cleanly when its context ends, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Start did not return after its context was cancelled")
	}
}

// A key the Host rejects gets nothing, and the name it would have carried is
// never learned by anyone.
func TestTheHostDecidesWhoGetsIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	stranger, _ := ssh.NewSignerFromKey(priv)

	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	host := &fakeHost{signer: hostSigner, allowed: map[string]string{}}

	srv := &sshServer{host: host, hostKey: hostSigner, logf: func(string, ...any) {},
		execFor: func(string) []string { return []string{"/bin/true"} }}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	_, err = ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            tunnelUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(stranger)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	})
	if err == nil {
		t.Error("a key the Host does not know must not open a tunnel")
	}
}

// Standalone is not a stub: it is what plug is today, and it must keep behaving
// that way - one shared key, and no identity claimed for it.
func TestStandaloneAuthenticatesTheSoftwareNotAPerson(t *testing.T) {
	_, pub := newTestKey(t)
	h := &standaloneHost{authorized: []ssh.PublicKey{pub}}

	who, ok := h.Verify(pub)
	if !ok {
		t.Fatal("the baked-in key must be accepted")
	}
	if who != "" {
		t.Errorf("standalone must claim no identity, got %q - that name would end up on a state page", who)
	}
}
