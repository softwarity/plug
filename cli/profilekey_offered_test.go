package main

// The one thing a user cannot check for themselves: that the key `plug pubkey`
// prints is the key `plug` actually presents.
//
// This is not a hypothetical invariant. It was broken, in the only way that
// matters: both halves were right on their own. keygen wrote the pair, pubkey
// printed it, the developer enrolled it and confirmed the fingerprint in the
// operator's database, and the tunnel offered the built-in key alone. The agent
// then refused a fingerprint that appeared nowhere in the story, and the person
// who had done everything correctly was told their key was not authorized.
//
// The cause was a process boundary: the LAUNCHER resolves the profile, the CORE
// opens the tunnel, and the config that crosses between them carried a host, a
// port and an update policy but not the key. Both processes were self-consistent.
// So these tests do not check the struct; they check what goes on the wire, and
// they check it across that boundary.

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// recordingAgent is an SSH server that refuses everyone and remembers every key
// it was offered, in order. Refusing is deliberate: it makes the client present
// ALL of its keys, which is exactly what has to be observed.
func recordingAgent(t *testing.T) (addr string, offered func() []string) {
	t.Helper()
	hostKey, err := ssh.NewSignerFromKey(mustGenerateKey(t))
	if err != nil {
		t.Fatal(err)
	}
	var mu chan []string = make(chan []string, 1)
	mu <- nil
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			seen := <-mu
			mu <- append(seen, ssh.FingerprintSHA256(key))
			return nil, errNotAuthorized
		},
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, _, _, err := ssh.NewServerConn(c, cfg)
				if err == nil {
					conn.Close()
				}
				c.Close()
			}()
		}
	}()
	return ln.Addr().String(), func() []string {
		seen := <-mu
		mu <- seen
		return seen
	}
}

var errNotAuthorized = &sshRefusal{}

type sshRefusal struct{}

func (*sshRefusal) Error() string { return "key is not authorized" }

// THE invariant. What pubkey prints for a profile must be the public half of
// what Dial presents for that same profile, and it must be presented FIRST:
// offering the shared key ahead of it would authenticate the software instead of
// the person on any agent that accepts both.
func TestWhatPubkeyPrintsIsWhatPlugOffers(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "neo")
	priv := profileKeyPath("neo")
	writeKeyPair("neo", priv, priv+".pub")
	setProfileKey("neo", "key", priv)

	published, err := os.ReadFile(priv + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(published)
	if err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(pub)

	addr, offered := recordingAgent(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg := loadProfile("neo")
	cfg.host, cfg.port = host, port

	if _, err := tunnel.Dial(host, port, sshUser, cfg.authKeys(), "", nil); err == nil {
		t.Fatal("the test agent refuses everyone; Dial must fail")
	}
	got := offered()
	if len(got) == 0 {
		t.Fatal("plug presented no key at all")
	}
	if got[0] != want {
		t.Fatalf("plug presented %s first, but 'plug pubkey' prints %s.\n"+
			"That gap is a developer enrolling one key and being refused for another.", got[0], want)
	}
	// And the shared key is still there behind it, or generating a key would cut
	// you off from every cluster that has not enrolled you.
	if len(got) != 2 {
		t.Fatalf("plug presented %d keys, want the profile's then the built-in one", len(got))
	}
}

// The same invariant, across the process boundary that actually broke it. The
// core is a SEPARATE process: whatever the launcher resolved reaches it through
// coreEnv and nothing else.
func TestTheProfileKeySurvivesTheLauncherToCoreExec(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "neo")
	priv := profileKeyPath("neo")
	writeKeyPair("neo", priv, priv+".pub")
	setProfileKey("neo", "key", priv)

	launcher := loadProfile("neo")
	launcher.host, launcher.port = "cluster.example", "2222"
	if launcher.key == "" {
		t.Fatal("the launcher itself lost the key")
	}

	// Exactly what the exec does: build the environment, then read it back the
	// way the core does, with nothing else carried over.
	for _, kv := range coreEnv(launcher) {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "PLUG_CORE") {
			t.Setenv(k, v)
		}
	}
	core := coreConfigFromEnv()

	if core.key != launcher.key {
		t.Fatalf("the core got key %q, the launcher had %q.\n"+
			"The core is the process that opens the tunnel: a key that stops here is never offered.",
			core.key, launcher.key)
	}
	if len(core.authKeys()) != 2 {
		t.Fatalf("the core offers %d keys, want the profile's and the built-in one", len(core.authKeys()))
	}
}

// A profile with no key of its own must still work, and offer exactly one key.
func TestAProfileWithNoKeyStillCrossesTheExec(t *testing.T) {
	sandboxHome(t)
	for _, k := range []string{"PLUG_CORE_HOST", "PLUG_CORE_PORT", "PLUG_CORE_KEY", "PLUG_CORE_UPDATE"} {
		t.Setenv(k, "")
	}
	for _, kv := range coreEnv(config{host: "cluster.example", port: "2222"}) {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "PLUG_CORE") {
			t.Setenv(k, v)
		}
	}
	core := coreConfigFromEnv()
	if core.key != "" {
		t.Errorf("a profile with no key produced key %q", core.key)
	}
	if len(core.authKeys()) != 1 {
		t.Errorf("offered %d keys, want just the built-in one", len(core.authKeys()))
	}
}

// A refused key stays refused. Telling that apart from a network blip is what
// stops the daemon turning a stated reason into three handshakes a second.
func TestAnAgentRefusingEveryKeyIsNotATransientFailure(t *testing.T) {
	addr, _ := recordingAgent(t)
	host, port, _ := net.SplitHostPort(addr)

	_, err := tunnel.Dial(host, port, sshUser, config{}.authKeys(), "", nil)
	if err == nil {
		t.Fatal("Dial must fail against an agent that refuses everyone")
	}
	if !tunnel.IsAuthFailure(err) {
		t.Fatalf("a refusal must be recognisable as one, got %v", err)
	}
	// And a host that is simply not there must NOT look like a refusal, or a
	// cluster that is merely down would stop being retried.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	dh, dp, _ := net.SplitHostPort(dead)
	_, err = tunnel.Dial(dh, dp, sshUser, config{}.authKeys(), "", nil)
	if err == nil {
		t.Skip("the port was reused between close and dial")
	}
	if tunnel.IsAuthFailure(err) {
		t.Errorf("an unreachable agent was read as a refusal: %v", err)
	}
}

// The refusal has to name the file, not just the fingerprint. The agent can only
// say "SHA256:… is not authorized"; it has no idea where that key came from, and
// a fingerprint the person cannot place is the whole reason this took an
// afternoon instead of ten seconds.
func TestTheRefusalNamesTheKeyFileAndNotJustAFingerprint(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "neo")
	priv := profileKeyPath("neo")
	writeKeyPair("neo", priv, priv+".pub")
	setProfileKey("neo", "key", priv)

	addr, _ := recordingAgent(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg := loadProfile("neo")
	cfg.host, cfg.port = host, port

	_, err := dialTunnel(cfg)
	if err == nil {
		t.Fatal("dialTunnel must fail against an agent that refuses everyone")
	}
	msg := err.Error()
	if !strings.Contains(msg, priv) {
		t.Errorf("the refusal does not name the key file %s:\n%s", priv, msg)
	}
	if !strings.Contains(msg, "plug pubkey") {
		t.Errorf("the refusal does not say what to enrol:\n%s", msg)
	}
	// Every key that was actually presented is accounted for by name.
	if strings.Count(msg, "SHA256:") != 2 {
		t.Errorf("want both offered fingerprints listed, got:\n%s", msg)
	}
}

func mustGenerateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// offeredBy runs one dial against a recording agent and returns the fingerprints
// that reached the wire, which is the only definition of "the identity plug
// presents" that cannot be argued with.
func offeredBy(t *testing.T, dial func(host, port string) error) []string {
	t.Helper()
	addr, offered := recordingAgent(t)
	host, port, _ := net.SplitHostPort(addr)
	if err := dial(host, port); err == nil {
		t.Fatal("the recording agent refuses everyone; the dial must fail")
	}
	return offered()
}

// THE invariant, stated the way the failure stated itself: `plug test -p neo`
// authenticated and the host named the developer, while the tunnel was refused
// in a loop with the shared key's fingerprint. One machine, one profile, two
// identities. Every path that composes from a profile must present the same
// list, in the same order.
func TestEveryPathOffersTheSameIdentityForOneProfile(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "neo")
	priv := profileKeyPath("neo")
	writeKeyPair("neo", priv, priv+".pub")
	setProfileKey("neo", "key", priv)

	// 1. What `plug test` presents: the launcher's own config, straight from the
	//    profile. This is the path that worked and hid the bug.
	fromTest := offeredBy(t, func(host, port string) error {
		cfg := loadProfile("neo")
		cfg.host, cfg.port = host, port
		_, err := tunnel.Dial(host, port, sshUser, cfg.authKeys(), "", nil)
		return err
	})

	// 2. What the TUNNEL presents, through the function every dial site uses.
	fromTunnel := offeredBy(t, func(host, port string) error {
		cfg := loadProfile("neo")
		cfg.host, cfg.port = host, port
		_, err := dialTunnel(cfg)
		return err
	})

	// 3. What the CORE presents: a separate process, rebuilt from the environment
	//    and nothing else. This is where the identity was being lost.
	fromCore := offeredBy(t, func(host, port string) error {
		launcher := loadProfile("neo")
		launcher.host, launcher.port = host, port
		for _, kv := range coreEnv(launcher) {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "PLUG_CORE") {
				t.Setenv(k, v)
			}
		}
		_, err := dialTunnel(coreConfigFromEnv())
		return err
	})

	for _, c := range []struct {
		name string
		got  []string
	}{{"the tunnel", fromTunnel}, {"the core", fromCore}} {
		if len(c.got) != len(fromTest) {
			t.Errorf("%s offered %d keys, `plug test` offered %d", c.name, len(c.got), len(fromTest))
			continue
		}
		for i := range c.got {
			if c.got[i] != fromTest[i] {
				t.Errorf("%s offered %s at position %d, `plug test` offered %s.\n"+
					"One profile cannot have two identities: whichever is wrong, the developer\n"+
					"is refused for a key they never chose.", c.name, c.got[i], i, fromTest[i])
			}
		}
	}
}

// The version that runs the tunnel is the CLUSTER's, not the launcher's. A core
// published before per-profile keys ignores PLUG_CORE_KEY, so handing off to it
// silently reverts to the shared key: `plug test` keeps working (it never leaves
// the launcher) while every tunnel is refused. The launcher must not hand off.
func TestAKeyedProfileNeverRunsACoreThatWouldDropItsKey(t *testing.T) {
	const key = "/home/dev/.plug/keys/neo"
	for _, c := range []struct {
		core, key string
		want      bool
		why       string
	}{
		{"2.11.0", key, true, "a published core from before the feature"},
		{"2.11.1", key, true, "the newest release at the time"},
		{"2.12.0", key, false, "the first core that reads the key"},
		{"3.0.0", key, false, "anything later"},
		{"2.11.0", "", false, "no profile key, nothing to drop"},
		{"dev", key, false, "a dev build is assumed to have it, like every other guard here"},
		{"dev+9f2a1c", key, false, "a branch build, same"},
		{"", key, false, "an agent that names no version at all"},
	} {
		if got := coreDropsProfileKey(c.core, c.key); got != c.want {
			t.Errorf("coreDropsProfileKey(%q, key=%q) = %v, want %v (%s)",
				c.core, c.key, got, c.want, c.why)
		}
	}
}

// The download channel carries the version, the digest and the BINARY that is
// then run with privilege. It used to ignore the agent's host key outright,
// while a comment two functions away claimed the answer arrived over an
// authenticated channel. Whatever the policy is, both channels must share it:
// one agent, recorded once, and a change noticed wherever it shows up first.
func TestTheDownloadChannelPinsLikeTheTunnel(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	dial := string(b)
	dial = dial[strings.Index(dial, "func dialGetUser("):]
	dial = dial[:strings.Index(dial, "\n}")]
	if strings.Contains(dial, "InsecureIgnoreHostKey") {
		t.Error("dialGetUser ignores the agent's host key, on the channel that delivers the binary plug runs as root")
	}
	if !strings.Contains(dial, "knownHostsFor(") {
		t.Error("dialGetUser does not use the same known_hosts choice as the tunnel")
	}
}

// The pin file is chosen the same way for both channels, and a loopback agent is
// deliberately exempt: there is no network to intercept, and a local dev agent
// recreated with a fresh key would otherwise raise a change on every restart.
func TestWhereTheHostKeyIsRecorded(t *testing.T) {
	sandboxHome(t)
	if got := knownHostsFor("127.0.0.1"); got != "" {
		t.Errorf("loopback should pin nothing, got %q", got)
	}
	if got := knownHostsFor("localhost"); got != "" {
		t.Errorf("loopback should pin nothing, got %q", got)
	}
	got := knownHostsFor("cluster.example")
	if got == "" {
		t.Fatal("a real host must have somewhere to record its key")
	}
	if filepath.Base(got) != "known_hosts" {
		t.Errorf("recorded in %q, want a known_hosts file", got)
	}
}

// The pin file is created by whichever channel dials FIRST, and on macOS that
// happens while plug is setuid root. So every writer of it must do the same two
// things: guard the path before (a symlinked ~/.plug must not aim the write
// somewhere else), and hand the file back to the user after.
//
// Pinning the download channel without the second step broke every macOS
// session in CI: this dial runs first, created ~/.plug/known_hosts owned by
// root, and the tunnel's own guard then refused it - correctly - as "a file
// outside your own tree". The feature was right and the ownership was not.
func TestBothChannelsGuardAndHandBackThePinFile(t *testing.T) {
	// Parsed, not sliced. This used to cut the file from "func dialGetUser(" to
	// the next "\n}" with strings.Index, which returns -1 when the function is
	// renamed or moved: the slice that follows then panics, so the test failed
	// with an index out of range instead of saying the thing it exists to say.
	// Its two siblings guard their extraction; this one relied on that panic.
	for _, want := range []struct{ file, fn string }{
		{"main.go", "dialGetUser"},
		{"socks_run.go", "dialTunnel"},
	} {
		bodies := funcBodies(t, want.file)
		body, ok := bodies[want.fn]
		if !ok {
			t.Fatalf("%s no longer defines %s. If it moved, move this test with it: what it checks is "+
				"that whoever creates the pin file guards the path before and hands the file back after, "+
				"and that has to be checked wherever the creating happens", want.file, want.fn)
		}
		for _, call := range []string{"guardUserPath", "chownToUser"} {
			if !strings.Contains(body, call) {
				t.Errorf("%s writes the pin file without calling %s: on macOS it runs first, as root, "+
					"and creates ~/.plug/known_hosts owned by root, which the tunnel's own guard then "+
					"refuses as a file outside your own tree", want.fn, call)
			}
		}
	}
}

// And the pinning actually happens: the callback the download channel now uses
// records the agent's key where that channel used to record nothing at all.
// Exercised through the exported callback rather than dialGetUser, because the
// test server listens on loopback and plug exempts loopback from pinning on
// purpose - there is no network to intercept, and a local dev agent recreated
// with a fresh key would raise a change on every restart.
func TestTheDownloadChannelsCallbackRecordsTheAgentsKey(t *testing.T) {
	dir := t.TempDir()
	pin := filepath.Join(dir, "known_hosts")
	addr, _ := recordingAgent(t)
	host, port, _ := net.SplitHostPort(addr)

	_, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            getUser,
		HostKeyCallback: tunnel.HostKeyCallback(pin, net.JoinHostPort(host, port), nil),
		Timeout:         2 * time.Second,
	})
	// The server refuses the account; what matters is that the host key callback
	// ran first and wrote what it saw.
	_ = err

	b, rerr := os.ReadFile(pin)
	if rerr != nil {
		t.Fatalf("nothing was recorded in %s: %v", pin, rerr)
	}
	if !strings.Contains(string(b), "ssh-ed25519") {
		t.Errorf("the pin file does not hold the agent's key: %q", b)
	}
}
