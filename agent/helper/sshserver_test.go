package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
