package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newTestKey returns a usable pair, signer side and authorized_keys side.
func newTestKey(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	return signer, sshPub
}

// startServer runs the server on a loopback port and returns its address. The
// forced command is a shell that echoes SSH_ORIGINAL_COMMAND, so a test can
// prove the request reached the account's command and nothing else.
func startServer(t *testing.T, host Host) string {
	t.Helper()
	return startServerWith(t, host, nil)
}

// startServerWith is startServer with one argv for every account, nil for the
// default that echoes the account and the request.
func startServerWith(t *testing.T, host Host, argv []string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	srv := &sshServer{
		host:    host,
		hostKey: hk,
		execFor: func(user string) []string {
			if argv != nil {
				return argv
			}
			// One script per account: whoever answers must be able to say which
			// command it was, which is what ForceCommand separation means.
			return []string{"/bin/sh", "-c", "printf '%s:%s' " + user + " \"$SSH_ORIGINAL_COMMAND\""}
		},
		logf: func(string, ...any) {},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func dial(t *testing.T, addr, user string, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

// The download account is anonymous by design, and its command receives the
// request verbatim. This is `ssh get@host install` and it must keep working
// without a key, or nobody can install the CLI that holds the key.
func TestDownloadUserNeedsNoKey(t *testing.T) {
	_, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	cl, err := dial(t, addr, downloadUser)
	if err != nil {
		t.Fatalf("anonymous download must connect: %v", err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("install")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := string(out); got != "get:install" {
		t.Errorf("the request must reach the download command verbatim, got %q", got)
	}
}

// The tunnel account is the one that gets a shell-less exec into the verbs, and
// only with a key the source accepts.
func TestTunnelUserNeedsAnAcceptedKey(t *testing.T) {
	signer, pub := newTestKey(t)
	strangerSigner, _ := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	if _, err := dial(t, addr, tunnelUser, ssh.PublicKeys(strangerSigner)); err == nil {
		t.Fatal("an unknown key must not open the tunnel")
	}
	if _, err := dial(t, addr, tunnelUser); err == nil {
		t.Fatal("the tunnel user must not connect without a key")
	}

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("an authorized key must connect: %v", err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("serve-name a 1:2 takeover")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := string(out); got != "plug:serve-name a 1:2 takeover" {
		t.Errorf("the verb must reach the tunnel command verbatim, got %q", got)
	}
}

// No shell, ever. A stolen key must buy exactly the verbs and nothing more,
// which is what ForceCommand bought us before.
func TestNoShellForEitherAccount(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	for _, c := range []struct {
		user string
		auth []ssh.AuthMethod
	}{
		{downloadUser, nil},
		{tunnelUser, []ssh.AuthMethod{ssh.PublicKeys(signer)}},
	} {
		cl, err := dial(t, addr, c.user, c.auth...)
		if err != nil {
			t.Fatalf("%s: connect: %v", c.user, err)
		}
		sess, err := cl.NewSession()
		if err != nil {
			t.Fatalf("%s: session: %v", c.user, err)
		}
		if err := sess.Shell(); err == nil {
			t.Errorf("%s got a shell", c.user)
		}
		if err := sess.RequestPty("xterm", 24, 80, nil); err == nil {
			t.Errorf("%s got a pty", c.user)
		}
		sess.Close()
		cl.Close()
	}
}

// The host key is the whole reason to write this server: OpenSSH regenerates it
// at every start, so the CLI re-pins whatever answers and the pin proves
// nothing. A key that survives a restart is what makes pinning worth doing.
func TestHostKeySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "host_key")
	first, err := hostKeySigner(path)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	again, err := hostKeySigner(path)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if ssh.FingerprintSHA256(first.PublicKey()) != ssh.FingerprintSHA256(again.PublicKey()) {
		t.Error("the host key must be the same across restarts, or pinning is theatre")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("host key is %04o: anyone who can read it can impersonate the agent", perm)
	}
}

func TestParseAuthorizedKeys(t *testing.T) {
	_, pub := newTestKey(t)
	line := string(ssh.MarshalAuthorizedKey(pub))

	got, err := parseAuthorizedKeys([]byte(line + "\n\n"))
	if err != nil || len(got) != 1 {
		t.Fatalf("one key with trailing blanks: %v (%d keys)", err, len(got))
	}
	// A typo that drops the only key must be loud, not silent: an empty
	// authorized list locks everyone out and would look like a network problem.
	if _, err := parseAuthorizedKeys([]byte("ssh-ed25519 not-base64\n")); err == nil {
		t.Error("a malformed line must be an error")
	}
	if _, err := parseAuthorizedKeys(nil); err == nil {
		t.Error("an empty file must be an error")
	}
	if _, err := parseAuthorizedKeys([]byte(strings.Repeat(" ", 8))); err == nil {
		t.Error("a blank file must be an error")
	}
}

// echoServer stands in for a cluster service: it answers whatever it is sent,
// prefixed, so a test can prove the bytes went all the way and came back.
func echoServer(t *testing.T, prefix string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 256)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				c.Write(append([]byte(prefix), buf[:n]...))
			}()
		}
	}()
	return ln
}

// direct-tcpip is the outbound half: `plug curl http://api:8080` becomes one of
// these, and the name is resolved by the agent, from inside the cluster.
func TestDirectTCPIPCarriesBytes(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})
	svc := echoServer(t, "svc:")

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()

	conn, err := cl.Dial("tcp", svc.Addr().String())
	if err != nil {
		t.Fatalf("direct-tcpip: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "svc:ping" {
		t.Errorf("bytes did not make the round trip, got %q", got)
	}
}

// An unreachable name must come back as a REFUSAL naming the cause, not as a
// channel that hangs: the CLI turns that refusal into a message the user can
// act on, which is most of what plug does when a name does not exist.
func TestDirectTCPIPRefusesWithACause(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()

	// Port 1 on the loopback: refused immediately, no DNS involved, no wait.
	_, err = cl.Dial("tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("dialling a closed port must fail")
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "connect") {
		t.Errorf("the refusal must carry the cause, got %q", err)
	}
}

// The download account may never forward, whatever it asks for. This is
// AllowTcpForwarding no, and it is what keeps an anonymous account from being a
// way into the cluster.
func TestDownloadUserCannotForward(t *testing.T) {
	_, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})
	svc := echoServer(t, "svc:")

	cl, err := dial(t, addr, downloadUser)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()

	if _, err := cl.Dial("tcp", svc.Addr().String()); err == nil {
		t.Error("the anonymous account opened a tunnel into the cluster")
	}
	if _, err := cl.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Error("the anonymous account bound a port in the agent")
	}
}

// tcpip-forward is what -s runs on: the agent binds a port and pushes every
// connection back to the developer's machine. This is the primitive the whole
// reverse direction depends on.
func TestRemoteForwardCarriesBytesBack(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()

	// Port 0: the server allocates and reports it back. Getting that reply
	// wrong means the CLI exposes a port nobody listens on.
	ln, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcpip-forward: %v", err)
	}
	defer ln.Close()
	bound := ln.Addr().(*net.TCPAddr).Port
	if bound == 0 {
		t.Fatal("the allocated port was not reported back")
	}

	// The developer's side: answer whatever the cluster sends.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		c.Write(append([]byte("local:"), buf[:n]...))
	}()

	// The cluster's side: something inside dials the name.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(bound)), 5*time.Second)
	if err != nil {
		t.Fatalf("nothing listening on the forwarded port: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte("hello"))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "local:hello" {
		t.Errorf("the reverse path did not carry the bytes, got %q", got)
	}
}

// A second session asking for a port the first one holds must be REFUSED. That
// refusal is what plug turns into "already exposed by another live session":
// silently accepting would give two sessions the same name and one of them no
// traffic.
func TestRemoteForwardRefusesATakenPort(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	first, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer first.Close()
	ln, err := first.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first forward: %v", err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port

	second, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer second.Close()
	if _, err := second.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(taken))); err == nil {
		t.Error("two sessions bound the same port")
	}
}

// The bind must be released when the connection ends, however it ends. sshd got
// this for free by owning a process; here nothing releases it unless the server
// does, and a leaked bind means the name cannot be re-armed until the agent
// restarts. This is the failure a crashed session would cause.
func TestBindsAreReleasedWhenTheConnectionDies(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ln, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// Drop the connection without cancelling the forward, as a killed session
	// would.
	cl.Close()

	// The release is asynchronous: give it a moment, then prove the port is free
	// by taking it.
	var lastErr error
	for i := 0; i < 50; i++ {
		probe, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		if err == nil {
			probe.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the bind outlived its connection, port %d still held: %v", port, lastErr)
}

// Cancelling releases the port and stops the listener, without touching the
// session: a mapping can be dropped and re-armed inside one connection.
func TestCancelReleasesTheBind(t *testing.T) {
	signer, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})

	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()
	ln, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil { // sends cancel-tcpip-forward
		t.Fatalf("cancel: %v", err)
	}
	for i := 0; i < 50; i++ {
		probe, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		if err == nil {
			probe.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("cancel did not free port %d", port)
}

// GatewayPorts clientspecified, the one behaviour the sshd_config calls out as
// the thing that must not be got wrong: the bind address the CLI names is the
// one used. Without it sshd bound the loopback, and the exposed port answered
// nobody from inside the cluster while the session looked perfectly healthy.
func TestForwardHonoursTheRequestedBindAddress(t *testing.T) {
	for _, want := range []string{"0.0.0.0", "127.0.0.1"} {
		f := &forwardSet{}
		port, err := f.open(ssh.Marshal(bindRequest{Addr: want, Port: 0}))
		if err != nil {
			t.Fatalf("%s: open: %v", want, err)
		}
		f.mu.Lock()
		ln := f.lns[key(want, port)]
		f.mu.Unlock()
		if ln == nil {
			t.Fatalf("%s: the forward was not keyed by the address the client named", want)
		}
		// Compare the PROPERTY, not the string: Go binds a wildcard as "::" in
		// dual-stack mode, which accepts IPv4 and IPv6 alike. Forcing "0.0.0.0"
		// here would look tidier and would quietly drop IPv6. What must never
		// happen is a loopback bind when the client asked for the wildcard: the
		// port would then answer nobody from inside the cluster, while the
		// session looked perfectly healthy.
		host, _, _ := net.SplitHostPort(ln.Addr().String())
		got, wanted := net.ParseIP(host), net.ParseIP(want)
		if got == nil || wanted == nil {
			t.Fatalf("%s: unparseable address %q", want, host)
		}
		if got.IsUnspecified() != wanted.IsUnspecified() {
			t.Errorf("client asked to bind %s, listener is on %s", want, host)
		}
		if wanted.IsLoopback() && !got.IsLoopback() {
			t.Errorf("client asked for loopback %s, listener is on %s", want, host)
		}
		f.closeAll()
	}
}

// SSH_CLIENT is what the name lease records (sessionOrigin, main.go), and what a
// collision message shows to tell a colleague's machine from your own. sshd set
// it; a Go server has to do it deliberately, and forgetting it empties that
// message without failing anything - the e2e cell only asserts that the refusal
// happens, not that it names an origin. Hence this test.
func TestTheForcedCommandSeesWhereTheSessionCameFrom(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	signer, pub := newTestKey(t)
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	srv := &sshServer{
		host:    &standaloneHost{authorized: []ssh.PublicKey{pub}},
		hostKey: hk,
		execFor: func(string) []string {
			return []string{"/bin/sh", "-c", `printf '%s|%s' "$SSH_CLIENT" "$SSH_CONNECTION"`}
		},
		logf: func(string, ...any) {},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	cl, err := dial(t, ln.Addr().String(), tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("serve-name x 1:2 takeover")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}

	parts := strings.SplitN(string(out), "|", 2)
	if len(parts) != 2 {
		t.Fatalf("expected both variables, got %q", out)
	}
	// SSH_CLIENT: "<client-ip> <client-port> <server-port>", and sessionOrigin
	// takes the first field, so an empty or malformed value costs the message.
	client := strings.Fields(parts[0])
	if len(client) != 3 {
		t.Fatalf("SSH_CLIENT must have three fields, got %q", parts[0])
	}
	if client[0] != "127.0.0.1" {
		t.Errorf("SSH_CLIENT must carry the client address, got %q", client[0])
	}
	// SSH_CONNECTION adds the server address: four fields.
	if conn := strings.Fields(parts[1]); len(conn) != 4 {
		t.Errorf("SSH_CONNECTION must have four fields, got %q", parts[1])
	}
}

// What sshd gave this port for free, and what had to be written back when it was
// replaced. Each of these was a way one stranger could cost the agent something
// no legitimate client ever asks for.

// hardeningServer starts a server configured BEFORE it serves: everything below
// tunes a field the accept loop reads, and setting it afterwards would be a data
// race the detector is right to flag.
func hardeningServer(t *testing.T, tune func(*sshServer)) string {
	t.Helper()
	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &sshServer{
		host: &fakeHost{signer: hostSigner, allowed: map[string]string{}}, hostKey: hostSigner,
		logf:    func(string, ...any) {},
		execFor: func(string) []string { return []string{"/bin/sh", "-c", "printf served"} },
	}
	if tune != nil {
		tune(srv)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return ln.Addr().String()
}

// A peer that connects and never speaks held a goroutine, a socket and a slot
// for as long as the agent lived. sshd called this LoginGraceTime.
func TestAHandshakeThatNeverFinishesIsDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	addr := hardeningServer(t, func(s *sshServer) { s.grace = 300 * time.Millisecond })

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Say nothing at all: no version banner, no key exchange.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	for {
		if _, err := c.Read(buf); err != nil {
			return // the server hung up on us, which is the point
		}
	}
}

// The grace must not outlive the handshake: `install` streams a binary over a
// link that can be slow, and a deadline left in place would cut it mid-download.
func TestTheGraceDoesNotSurviveIntoTheSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	addr := hardeningServer(t, func(s *sshServer) { s.grace = 1 * time.Second })

	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	// Idle past the grace, then use the connection. A deadline still in force
	// would have killed it while it sat there.
	time.Sleep(2 * time.Second)
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatalf("the session died with the handshake grace still on it: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Output("version"); err != nil {
		t.Fatalf("exec after idling past the grace: %v", err)
	}
}

// A panic while serving one stranger must cost that connection and nothing else.
// sshd answered for this with a process per connection; here it is one goroutine
// inside a gateway that has other work to do.
func TestAPanickingConnectionDoesNotTakeTheAgentDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	var boom atomic.Bool
	boom.Store(true)
	addr := hardeningServer(t, func(s *sshServer) {
		s.execFor = func(string) []string {
			if boom.Load() {
				panic("the verb lookup blew up")
			}
			return []string{"/bin/sh", "-c", "printf served"}
		}
	})

	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if sess, serr := cl.NewSession(); serr == nil {
		_, _ = sess.Output("version") // panics inside the server
		sess.Close()
	}
	cl.Close()

	// The agent is still there for the next caller.
	boom.Store(false)
	cl2, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("one connection's panic took the whole agent down: %v", err)
	}
	defer cl2.Close()
	sess2, err := cl2.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()
	out, err := sess2.Output("version")
	if err != nil || string(out) != "served" {
		t.Fatalf("after the panic the agent answered %q, %v", out, err)
	}
}

// A handshake that panics must give its slot back.
//
// The slot exists to bound how many unauthenticated peers can be mid-handshake
// at once (MaxStartups). It was taken before ssh.NewServerConn and returned by a
// plain call after it - and NewServerConn is exactly the function that parses
// what a stranger sent, which is why the recover around it exists at all. A
// panic there was absorbed, the connection closed, and the slot never came back.
// Enough of them and the server refuses every new connection for good, while
// still logging that it is ready.
func TestAPanickingHandshakeGivesItsSlotBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	var boom atomic.Bool
	boom.Store(true)

	hostSigner, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	clientPub, _ := ssh.NewPublicKey(pub)
	clientSigner, _ := ssh.NewSignerFromKey(priv)

	srv := &sshServer{
		hostKey: hostSigner, logf: func(string, ...any) {},
		execFor: func(string) []string { return []string{"/bin/sh", "-c", "printf served"} },
		// Two slots, so two panics are enough to prove the leak. Sixty-five
		// connections would prove the same thing and say it less clearly.
		maxInFlight: 2,
		host: &panicHost{
			signer:  hostSigner,
			allowed: map[string]string{ssh.FingerprintSHA256(clientPub): "alice"},
			boom:    &boom,
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	addr := ln.Addr().String()

	// Burn every slot with a handshake that blows up inside NewServerConn.
	for i := 0; i < 4; i++ {
		_, _ = ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            tunnelUser,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         2 * time.Second,
		})
	}

	// The agent must still take a connection. If the slots leaked, this is
	// refused before the handshake even starts.
	boom.Store(false)
	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            downloadUser,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatalf("after 4 panicking handshakes with 2 slots, the agent takes nothing: %v", err)
	}
	cl.Close()
}

// The ceilings (maxForwards and the two `get` ceilings). Each is lowered to a
// handful here: proving that the 257th forward is refused would take 256
// sockets to say what three say as well. What matters in each case is the
// same three facts: the request past the ceiling is refused with a reason the
// client sees, what was already open is untouched, and releasing one makes
// room for the next.

func TestRemoteForwardsAreCappedPerConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	signer, pub := newTestKey(t)
	addr := hardeningServer(t, func(s *sshServer) {
		s.maxFwd = 3
		s.host = &fakeHost{signer: s.hostKey, allowed: map[string]string{ssh.FingerprintSHA256(pub): "alice"}}
	})
	cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close()

	var held []net.Listener
	for i := 0; i < 3; i++ {
		ln, err := cl.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("forward %d, under the ceiling: %v", i+1, err)
		}
		held = append(held, ln)
	}
	if ln, err := cl.Listen("tcp", "127.0.0.1:0"); err == nil {
		ln.Close()
		t.Fatal("the forward past the ceiling was accepted")
	}
	// The three already open still answer: a refusal costs nothing to them.
	for _, ln := range held {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatalf("a forward below the ceiling stopped answering: %v", err)
		}
		c.Close()
	}
	// Cancelling one makes room for one.
	held[0].Close()
	ln, err := cl.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("after a cancel the next forward must be taken: %v", err)
	}
	ln.Close()
	// A second connection has a ceiling of its own: the count is per session,
	// not per agent.
	other, err := dial(t, addr, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer other.Close()
	if ln, err := other.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatalf("another connection's forward must not count against this one: %v", err)
	} else {
		ln.Close()
	}
}

// openSessions opens sessions until one is refused or n were opened,
// returning the ones that opened and the refusal.
func openSessions(cl *ssh.Client, n int) ([]*ssh.Session, error) {
	var open []*ssh.Session
	for i := 0; i < n; i++ {
		sess, err := cl.NewSession()
		if err != nil {
			return open, err
		}
		open = append(open, sess)
	}
	return open, nil
}

// waitSession retries NewSession briefly: the server counts a session down
// when its channel closes, which lands a moment after the client's Close.
func waitSession(cl *ssh.Client) (*ssh.Session, error) {
	var err error
	for i := 0; i < 100; i++ {
		var sess *ssh.Session
		if sess, err = cl.NewSession(); err == nil {
			return sess, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, err
}

func TestDownloadSessionsAreCappedPerConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	addr := hardeningServer(t, func(s *sshServer) { s.maxGetSess = 2 })
	cl, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	open, err := openSessions(cl, 2)
	if err != nil {
		t.Fatalf("under the ceiling: %v", err)
	}
	third, err := cl.NewSession()
	if err == nil {
		third.Close()
		t.Fatal("the session past the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), "sessions open") {
		t.Fatalf("the refusal must say why, got %v", err)
	}
	// The two below it still run their command.
	out, err := open[0].Output("version")
	if err != nil || string(out) != "served" {
		t.Fatalf("a session under the ceiling was hurt by the refusal: %q, %v", out, err)
	}
	// That one is over now (its command exited): room for one more.
	open[0].Close()
	sess, err := waitSession(cl)
	if err != nil {
		t.Fatalf("after a session ended the next must be taken: %v", err)
	}
	sess.Close()
	open[1].Close()
}

func TestDownloadConnectionsAreCappedAcrossTheAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	addr := hardeningServer(t, func(s *sshServer) { s.maxGetConns = 1 })
	cfg := &ssh.ClientConfig{User: downloadUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second}
	first, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// The slot is taken by the first session, not by the connection: a
	// stranger who connects and says nothing holds none.
	sess, err := first.NewSession()
	if err != nil {
		t.Fatalf("the first download connection must get its session: %v", err)
	}
	defer sess.Close()

	second, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("the connection itself is not refused, only its sessions: %v", err)
	}
	defer second.Close()
	if s2, err := second.NewSession(); err == nil {
		s2.Close()
		t.Fatal("a second download connection got a session with one slot")
	} else if !strings.Contains(err.Error(), "retry shortly") {
		t.Fatalf("the refusal must tell the client to retry, got %v", err)
	}
	// The first still works: the refusal is the stranger's, not the holder's.
	if out, err := sess.Output("version"); err != nil || string(out) != "served" {
		t.Fatalf("the holder was hurt by the refusal: %q, %v", out, err)
	}
	// Once the holder leaves, the waiting connection gets its session on the
	// same connection: no reconnect needed.
	first.Close()
	s2, err := waitSession(second)
	if err != nil {
		t.Fatalf("after the holder left the next connection must be served: %v", err)
	}
	s2.Close()

	// The tunnel account is not counted: a key the Host accepted opens
	// sessions whatever the download traffic.
	signer, pub := newTestKey(t)
	keyed := hardeningServer(t, func(s *sshServer) {
		s.maxGetConns = 1
		s.host = &fakeHost{signer: s.hostKey, allowed: map[string]string{ssh.FingerprintSHA256(pub): "alice"}}
	})
	holder, err := ssh.Dial("tcp", keyed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	hs, err := holder.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	tun, err := dial(t, keyed, tunnelUser, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	ts, err := tun.NewSession()
	if err != nil {
		t.Fatalf("the tunnel account must not be bounded by the download slots: %v", err)
	}
	ts.Close()
}

// panicHost blows up inside Verify, which runs inside ssh.NewServerConn.
type panicHost struct {
	signer  ssh.Signer
	allowed map[string]string
	boom    *atomic.Bool
}

func (h *panicHost) HostKey() (ssh.Signer, error) { return h.signer, nil }
func (h *panicHost) Verify(key ssh.PublicKey) (string, bool) {
	if h.boom.Load() {
		panic("the key store blew up mid-handshake")
	}
	who, ok := h.allowed[ssh.FingerprintSHA256(key)]
	return who, ok
}
func (h *panicHost) Served(NameEvent) {}
func (h *panicHost) Unserved(string)  {}

// `ssh get@host install | sh` from a terminal: the client's stdin is the
// keyboard, which sends nothing and never ends. The command is over the moment
// it exits, and the session must end with it. It did not: exec.Cmd waits for
// the goroutine copying a non-file Stdin, which waited for the keyboard, so the
// install sat there finished until someone pressed Enter. Would have caught:
// Stdin handed to exec.Cmd as the channel itself.
func TestTheSessionEndsWithTheCommandWhateverStdinDoes(t *testing.T) {
	_, pub := newTestKey(t)
	addr := startServer(t, &standaloneHost{authorized: []ssh.PublicKey{pub}})
	cl, err := dial(t, addr, downloadUser)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	keyboard, typing := io.Pipe() // open, silent: a terminal nobody types in
	defer typing.Close()
	sess.Stdin = keyboard
	var out bytes.Buffer
	sess.Stdout = &out
	done := make(chan error, 1)
	go func() { done <- sess.Run("install") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command exited but the session waited for stdin: a piped install hangs until Enter")
	}
	if got := out.String(); got != "get:install" {
		t.Errorf("output: %q", got)
	}
}

// What the command reads on stdin still reaches it: the fix must not cut the
// input of a verb that wants one.
func TestStdinStillReachesTheCommand(t *testing.T) {
	_, pub := newTestKey(t)
	addr := startServerWith(t, &standaloneHost{authorized: []ssh.PublicKey{pub}}, []string{"/bin/sh", "-c", "cat"})
	cl, err := dial(t, addr, downloadUser)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader("hello\n")
	out, err := sess.Output("anything")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if string(out) != "hello\n" {
		t.Errorf("stdin did not reach the command: %q", out)
	}
}

// What the embedder tells the installer (KeyInstruction) reaches the download
// account's command, and only that account's: the verbs have no use for it.
func TestTheInstallerIsToldWhatTheEmbedderDecided(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, pub := newTestKey(t)
	srv := &sshServer{
		host:    &standaloneHost{authorized: []ssh.PublicKey{pub}},
		hostKey: hk,
		execFor: func(string) []string {
			return []string{"/bin/sh", "-c", `printf '%s' "$PLUG_KEY_INSTRUCTION"`}
		},
		logf:       func(string, ...any) {},
		installEnv: []string{keyInstructionEnv + "=register the key it prints in your dev profile"},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer ln.Close()
	run := func(user string, auth ...ssh.AuthMethod) string {
		cl, err := dial(t, ln.Addr().String(), user, auth...)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		sess, err := cl.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		out, err := sess.Output("install")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run(downloadUser); got != "register the key it prints in your dev profile" {
		t.Fatalf("the installer was told %q", got)
	}
	if got := run(tunnelUser, ssh.PublicKeys(signer)); got != "" {
		t.Fatalf("a verb was told the installer's sentence: %q", got)
	}
}
