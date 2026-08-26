package agent

// The decisions an embedder makes and the agent cannot: which image a signpost
// runs, what version this agent is, whether it may update its own deployment,
// and whether the anonymous download account exists at all.
//
// Each of these defaults to what a standalone agent has always done, and each
// was a silent breakage for a gateway before it was a field.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A standalone agent IS the plug image, so its own image carries the binary the
// signpost entrypoint runs. A gateway is not, and a signpost built from its
// image would die instantly on a missing /usr/local/bin/plug-agent.
func TestTheSignpostImageCanBeTheEmbeddersChoice(t *testing.T) {
	t.Setenv(signpostImageEnv, "")
	if got := signpostImage("softwarity/plug:2.11.0"); got != "softwarity/plug:2.11.0" {
		t.Errorf("with no override the signpost runs the agent's own image, got %q", got)
	}
	t.Setenv(signpostImageEnv, "softwarity/plug:2.11.0")
	if got := signpostImage("acme/meerkat:4.2"); got != "softwarity/plug:2.11.0" {
		t.Errorf("the embedder's image must win, got %q", got)
	}
	// Whitespace from a YAML value that ended up quoted oddly is not an image.
	t.Setenv(signpostImageEnv, "   ")
	if got := signpostImage("acme/meerkat:4.2"); got != "acme/meerkat:4.2" {
		t.Errorf("a blank override must not become the image, got %q", got)
	}
}

// The version is not cosmetic: the CLI turns it into a cache path and refuses to
// run a core whose digest this agent cannot vouch for. An embedder has no
// /opt/plug/VERSION, so without this field every client fails on "the agent
// could not tell what vunknown should hash to".
func TestTheVersionComesFromTheEmbedderWhenThereIsNoFile(t *testing.T) {
	t.Setenv(versionEnv, "2.11.0")
	if got := localVersion(); got != "2.11.0" {
		t.Errorf("localVersion() = %q, want what the embedder declared", got)
	}
	t.Setenv(versionEnv, "  2.12.0  ")
	if got := localVersion(); got != "2.12.0" {
		t.Errorf("localVersion() = %q, want it trimmed", got)
	}
	// Empty falls through to the file, which is the standalone path, and to
	// "unknown" when there is no file either. Both are the old behaviour.
	t.Setenv(versionEnv, "")
	if _, err := os.Stat(versionFile); err != nil {
		if got := localVersion(); got != "unknown" {
			t.Errorf("localVersion() = %q, want unknown with no file and no override", got)
		}
	}
}

// newTestServer builds a server the way Start does, without the listener dance.
func newTestServer(t *testing.T, cfg Config) (*sshServer, string, ssh.Signer) {
	t.Helper()
	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	clientPub, _ := ssh.NewPublicKey(pub)
	host := &fakeHost{signer: hostSigner, allowed: map[string]string{
		ssh.FingerprintSHA256(clientPub): "alice",
	}}
	srv := &sshServer{
		host: host, hostKey: hostSigner, logf: func(string, ...any) {},
		execFor: func(user string) []string {
			if user == downloadUser {
				return []string{"/bin/sh", "-c", "printf downloads"}
			}
			return cfg.VerbCommand
		},
		verbEnv: []string{
			versionEnv + "=" + cfg.Version,
			signpostImageEnv + "=" + cfg.SignpostImage,
			noSelfUpdateEnv + "=" + boolEnv(cfg.NoSelfUpdate),
		},
		noDownloadAccount: cfg.NoDownloadAccount,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	signer, _ := ssh.NewSignerFromKey(priv)
	return srv, ln.Addr().String(), signer
}

// The account is anonymous by design, and that design is a surface the embedder
// may not want on its own port. Closing it must close it, and must not announce
// which agents have it.
func TestTheEmbedderCanCloseTheAnonymousAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	_, open, _ := newTestServer(t, Config{})
	cl, err := ssh.Dial("tcp", open, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("the download account is open by default and did not answer: %v", err)
	}
	cl.Close()

	_, closed, _ := newTestServer(t, Config{NoDownloadAccount: true})
	_, err = ssh.Dial("tcp", closed, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("the embedder closed the download account and it still let a client in")
	}
	// The refusal is the same one an unknown user gets, and SSH never relays the
	// server's reason anyway: the client is told only that no method remains. So
	// the port cannot tell an outsider which agents carry the account.
	if strings.Contains(err.Error(), "download") || strings.Contains(err.Error(), "closed") {
		t.Errorf("the refusal describes the account: %q", err)
	}
}

// A verb runs in another process and can ask the Host nothing, so every decision
// above has to arrive through the environment. This asserts the wiring end to
// end rather than the fields in isolation.
func TestTheEmbedderDecisionsReachTheVerb(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	_, addr, signer := newTestServer(t, Config{
		Version:       "2.11.0",
		SignpostImage: "softwarity/plug:2.11.0",
		NoSelfUpdate:  true,
		VerbCommand: []string{"/bin/sh", "-c",
			`printf 'v=%s img=%s nsu=%s' "$PLUG_VERSION" "$PLUG_SIGNPOST_IMAGE" "$PLUG_NO_SELF_UPDATE"`},
	})
	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            tunnelUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("info")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	want := "v=2.11.0 img=softwarity/plug:2.11.0 nsu=1"
	if got := string(out); got != want {
		t.Errorf("the verb saw %q, want %q", got, want)
	}
}

// Left alone, an embedder gets exactly what a standalone agent has always had.
func TestTheDefaultsAreTodaysBehaviour(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	_, addr, signer := newTestServer(t, Config{
		VerbCommand: []string{"/bin/sh", "-c",
			`printf 'v=[%s] img=[%s] nsu=%s' "$PLUG_VERSION" "$PLUG_SIGNPOST_IMAGE" "$PLUG_NO_SELF_UPDATE"`},
	})
	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            tunnelUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()
	sess, _ := cl.NewSession()
	defer sess.Close()
	out, _ := sess.Output("info")
	// Empty means "fall through to the file / to the agent's own image", which
	// is what every deployed agent does today.
	if got := string(out); got != "v=[] img=[] nsu=0" {
		t.Errorf("defaults changed: verb saw %q", got)
	}
}

// Start must not need a Config to keep working, since that is what every
// deployed agent passes today.
func TestStartStillNeedsNothingButAHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Start(ctx, Config{
			Host: &fakeHost{signer: hostSigner, allowed: map[string]string{}},
			Addr: addr,
			Logf: func(string, ...any) {},
		})
	}()
	// Reachable, then gone when its context ends.
	var up bool
	for i := 0; i < 100 && !up; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			up = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		cancel()
		t.Fatal("Start with only a Host never came up")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Start did not return after its context was cancelled")
	}
}
