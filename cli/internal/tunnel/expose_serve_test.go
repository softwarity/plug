package tunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeExposeTransport is the SSH transport under an armed mapping: the reverse
// path is a loop between a listener the agent hands us and a dial back into the
// cluster, and both ends belong to the transport.
type fakeExposeTransport struct {
	reconnect func(stale *ssh.Client) (*ssh.Client, error)
	dial      func(addr string, d time.Duration) (net.Conn, error)

	mu    sync.Mutex
	notes []string
}

func (f *fakeExposeTransport) reconnectFrom(stale *ssh.Client) (*ssh.Client, error) {
	if f.reconnect == nil {
		return nil, errClosed
	}
	return f.reconnect(stale)
}

func (f *fakeExposeTransport) DialClusterTimeout(addr string, d time.Duration) (net.Conn, error) {
	if f.dial == nil {
		return nil, errors.New("no cluster dialler in this test")
	}
	return f.dial(addr, d)
}

func (f *fakeExposeTransport) note(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, fmt.Sprintf(format, a...))
}

func (f *fakeExposeTransport) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notes...)
}

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// chanListener is a remote forward's listener: flows a test pushes in come out
// of Accept, and Close makes Accept fail exactly as the SSH connection dying
// under the forward does.
type chanListener struct {
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	addr   net.Addr
	closed atomic.Bool
}

func newChanListener() *chanListener {
	return &chanListener{conns: make(chan net.Conn, 4), done: make(chan struct{}), addr: fakeAddr("0.0.0.0:41001")}
}

func (l *chanListener) push(c net.Conn) { l.conns <- c }

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, errors.New("forward listener closed")
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { l.closed.Store(true); close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// fakeBinder is the sshd side of a tcpip-forward request.
type fakeBinder struct {
	ln       net.Listener
	err      error
	network  string
	bindAddr string
}

func (b *fakeBinder) Listen(n, addr string) (net.Listener, error) {
	b.network, b.bindAddr = n, addr
	return b.ln, b.err
}

func stubListenRemote(t *testing.T, fn func(remoteBinder, ExposeSpec) (net.Listener, string, error)) {
	t.Helper()
	prev := listenRemote
	listenRemote = fn
	t.Cleanup(func() { listenRemote = prev })
}

// shortRearm scales the re-bind pacing down so a test drives hundreds of rounds
// in the time a session spends on one.
func shortRearm(t *testing.T) {
	t.Helper()
	prev := exposeRearmEvery
	exposeRearmEvery = time.Millisecond
	t.Cleanup(func() { exposeRearmEvery = prev })
}

func waitFor(t *testing.T, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func testSpec(port string) ExposeSpec {
	return ExposeSpec{Name: "mail", ClusterPort: "80", LocalPort: port}
}

// The forward binds an ALLOCATED port, never the cluster port: all of a session's
// names converge on the one agent container, where a fixed port could be bound
// once and every later session would be refused.
func TestListenRemoteAsksSshdToAllocateThePort(t *testing.T) {
	l := newChanListener()
	l.addr = fakeAddr("0.0.0.0:41337")
	b := &fakeBinder{ln: l}
	ln, port, err := listenRemote(b, testSpec("3000"))
	if err != nil {
		t.Fatalf("listenRemote: %v", err)
	}
	if ln != net.Listener(l) {
		t.Fatal("listenRemote returned a listener other than sshd's")
	}
	if port != "41337" {
		t.Fatalf("port %q, want the one sshd allocated (41337)", port)
	}
	if b.bindAddr != "0.0.0.0:0" {
		t.Fatalf("bound %q; binding anything but port 0 makes the second session on a name fail", b.bindAddr)
	}
}

// An agent image whose sshd cannot allocate a port answers with port 0. Taking
// that at face value would build a signpost relaying to :0, which fails much
// later and looks like a broken name rather than an old agent.
func TestListenRemoteRejectsAnUnusableAllocation(t *testing.T) {
	l := newChanListener()
	l.addr = fakeAddr("0.0.0.0:0")
	b := &fakeBinder{ln: l}
	_, _, err := listenRemote(b, testSpec("3000"))
	if err == nil {
		t.Fatal("port 0 was accepted as an allocation")
	}
	if !strings.Contains(err.Error(), "mail:80") {
		t.Fatalf("the error does not name the mapping: %v", err)
	}
	if !l.closed.Load() {
		t.Fatal("the unusable forward was left open on the agent")
	}
}

// A refused bind is the loud case: another session already holds the name.
func TestListenRemoteNamesTheMappingWhenTheAgentRefuses(t *testing.T) {
	b := &fakeBinder{err: errors.New("ssh: tcpip-forward request denied by peer")}
	_, _, err := listenRemote(b, testSpec("3000"))
	if err == nil {
		t.Fatal("a refused forward was reported as success")
	}
	if !strings.Contains(err.Error(), "mail:80") || !strings.Contains(err.Error(), "denied by peer") {
		t.Fatalf("the error loses either the mapping or the cause: %v", err)
	}
}

// A mapping that cannot even be armed must fail the session, not start it
// half-working: a name nobody can reach looks like a cluster problem for as
// long as it takes someone to notice plug never bound anything.
func TestExposeFailsLoudWhenTheTransportCannotConnect(t *testing.T) {
	tr := &Transport{} // no keys, never dialled
	if _, err := tr.Expose(testSpec("3000")); err == nil {
		t.Fatal("Expose reported success with no connection to the agent")
	}
}

// The whole self-heal contract of the reverse path: the connection under the
// forward dies, the mapping re-binds on a NEW allocated port, and the
// re-provisioner is signalled once so it rebuilds the signpost against that
// port. Signalled too early it publishes the dead port; signalled per member it
// rebuilds the signpost N times (about 8.5s each on Swarm).
func TestServeRearmsOnANewPortAndSignalsTheReprovisionerOnce(t *testing.T) {
	shortRearm(t)
	port, reached := localService(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	first, second := newChanListener(), newChanListener()

	var binds atomic.Int32
	stubListenRemote(t, func(remoteBinder, ExposeSpec) (net.Listener, string, error) {
		if n := binds.Add(1); n > 1 {
			t.Errorf("one dead connection caused %d re-binds", n)
		}
		return second, "41002", nil
	})
	var ending atomic.Bool
	tr := &fakeExposeTransport{reconnect: func(*ssh.Client) (*ssh.Client, error) {
		if ending.Load() {
			return nil, errClosed
		}
		return nil, nil
	}}
	e := &Exposed{t: tr, spec: testSpec(port), agentPort: "41001", hit: make(chan struct{}, 1)}
	var hooks atomic.Int32
	seen := make(chan string, 4)
	e.OnRearm(func() { hooks.Add(1); seen <- e.AgentPort() })

	served := make(chan struct{})
	go func() { e.serve(first, nil); close(served) }()

	first.Close() // the SSH connection under the forward is gone

	select {
	case got := <-seen:
		if got != "41002" {
			t.Fatalf("the re-provisioner read port %q; it rebuilds the signpost from AgentPort, so the new port must be published before the signal", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the forward died and nothing signalled the re-provisioner: a dynamic name stays pointed at the dead port")
	}
	if got := e.AgentPort(); got != "41002" {
		t.Fatalf("AgentPort is %q after re-arming, want 41002", got)
	}

	// Armed on paper is not armed: the re-armed listener has to be the one the
	// accept loop is now serving.
	client, server := tcpPair(t)
	defer client.Close()
	second.push(server)
	waitFor(t, func() bool { return reached.Load() == 1 }, "nothing was accepted on the re-armed forward")

	if n := hooks.Load(); n != 1 {
		t.Fatalf("the re-provisioner was signalled %d times for one reconnect", n)
	}
	var told bool
	for _, n := range tr.recorded() {
		if strings.Contains(n, "re-armed") && strings.Contains(n, "mail:80 -> localhost:"+port) {
			told = true
		}
	}
	if !told {
		t.Fatalf("the reconnect went unreported: %q", tr.recorded())
	}

	ending.Store(true)
	second.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serve outlived the closed transport")
	}
}

// After a rough disconnect the agent's sshd can hold the dead bind for about 90
// seconds, and a port another session took can free up hours later. Giving up
// on the first refusal leaves the mapping dead for the rest of the session with
// nothing to say so.
func TestRearmKeepsRetryingUntilTheForwardComesBack(t *testing.T) {
	shortRearm(t)
	var dials, binds atomic.Int32
	tr := &fakeExposeTransport{reconnect: func(*ssh.Client) (*ssh.Client, error) {
		if dials.Add(1) < 3 {
			return nil, errors.New("dial tcp: no route to host") // network still down
		}
		return nil, nil
	}}
	want := newChanListener()
	stubListenRemote(t, func(remoteBinder, ExposeSpec) (net.Listener, string, error) {
		if binds.Add(1) < 3 {
			return nil, "", errors.New("ssh: tcpip-forward request denied by peer") // bind still held
		}
		return want, "41042", nil
	})
	e := &Exposed{t: tr, spec: testSpec("3000"), agentPort: "41001", hit: make(chan struct{}, 1)}

	ln, _, err := e.rearm(nil)
	if err != nil {
		t.Fatalf("rearm gave up on a mapping that came back: %v", err)
	}
	if ln != net.Listener(want) {
		t.Fatal("rearm returned a listener other than the one it just bound")
	}
	if d, b := dials.Load(), binds.Load(); d < 3 || b < 3 {
		t.Fatalf("rearm stopped early (%d reconnects, %d binds): both a down network and a held bind must be retried", d, b)
	}
	if got := e.AgentPort(); got != "41042" {
		t.Fatalf("AgentPort is %q, want the port the successful re-bind allocated", got)
	}
}

// The one thing that DOES end the loop. Without it every -s mapping keeps a
// goroutine re-dialling an agent the session has already left.
func TestRearmEndsOnlyWhenTheSessionCloses(t *testing.T) {
	// No shortRearm here on purpose: a closed transport must be recognised on
	// the first attempt, before the loop ever paces itself.
	tr := &fakeExposeTransport{reconnect: func(*ssh.Client) (*ssh.Client, error) {
		return nil, fmt.Errorf("transport gone: %w", errClosed)
	}}
	e := &Exposed{t: tr, spec: testSpec("3000"), hit: make(chan struct{}, 1)}

	done := make(chan error, 1)
	go func() { _, _, err := e.rearm(nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errClosed) {
			t.Fatalf("rearm ended with %v, want the closed-transport error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rearm kept retrying a closed transport")
	}
}

// A re-arm wave is genuinely concurrent: every member re-binds on its own
// goroutine while the single re-provisioner reads all their ports and
// re-registers hooks. Unguarded, the port a signpost is built from is a torn
// read, and the hook a reconnect picks up can be a half-written one.
func TestRearmWaveTouchesSharedStateUnderTheLock(t *testing.T) {
	shortRearm(t)
	const rounds = 200
	var armed atomic.Int32
	stubListenRemote(t, func(remoteBinder, ExposeSpec) (net.Listener, string, error) {
		l := newChanListener()
		l.Close() // dead on arrival, so serve goes straight round to re-arming
		return l, fmt.Sprintf("41%03d", armed.Add(1)%1000), nil
	})
	tr := &fakeExposeTransport{reconnect: func(*ssh.Client) (*ssh.Client, error) {
		if armed.Load() >= rounds {
			return nil, errClosed
		}
		return nil, nil
	}}
	e := &Exposed{t: tr, spec: testSpec("3000"), agentPort: "41001", hit: make(chan struct{}, 1)}
	e.OnRearm(func() {})

	first := newChanListener()
	first.Close()
	served := make(chan struct{})
	go func() { e.serve(first, nil); close(served) }()

	reprovisioned := make(chan struct{})
	go func() { // the re-provisioner side, as socks_run.go runs it
		defer close(reprovisioned)
		for i := 0; i < rounds*5; i++ {
			_ = e.AgentPort()
			e.OnRearm(func() {})
		}
	}()

	<-reprovisioned
	select {
	case <-served:
	case <-time.After(20 * time.Second):
		t.Fatal("serve never noticed the closed transport")
	}
}

// The end-to-end claim Verify makes: the nonce goes out through the cluster
// name and comes back on THIS session's accept loop. Anything weaker (a dial
// that merely connects) would pass while another container answers the name.
func TestVerifyProvesTheLoopBackToThisSession(t *testing.T) {
	port, reached := localService(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	ln := newChanListener()
	var dialled string
	var budget time.Duration
	tr := &fakeExposeTransport{
		reconnect: func(*ssh.Client) (*ssh.Client, error) { return nil, errClosed },
		dial: func(addr string, d time.Duration) (net.Conn, error) {
			dialled, budget = addr, d
			client, server := tcpPair(t)
			ln.push(server) // sshd relaying the flow into our forward
			return client, nil
		},
	}
	e := &Exposed{t: tr, spec: testSpec(port), agentPort: "41001", hit: make(chan struct{}, 1)}
	served := make(chan struct{})
	go func() { e.serve(ln, nil); close(served) }()

	if err := e.Verify(2 * time.Second); err != nil {
		t.Fatalf("Verify rejected its own loopback: %v", err)
	}
	if dialled != "mail:80" {
		t.Fatalf("Verify dialled %q; it has to resolve the name exactly as a cluster workload would", dialled)
	}
	if budget != 2*time.Second {
		t.Fatalf("the dial got a %v budget instead of the caller's: a caller that retries must be able to probe cheaply", budget)
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the probe reached the local service %d time(s)", n)
	}

	ln.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serve outlived the closed transport")
	}
}

// A hit buffered by an attempt that had already timed out satisfies the next
// attempt's select without any nonce coming back: the retry loop in socks_run.go
// makes that the normal case, so a name answered by something else would be
// declared verified on the second try.
func TestVerifyDoesNotPassOnAHitFromAnEarlierAttempt(t *testing.T) {
	tr := &fakeExposeTransport{dial: func(string, time.Duration) (net.Conn, error) {
		client, _ := tcpPair(t) // nothing reads the nonce: another container answering the name
		return client, nil
	}}
	e := &Exposed{t: tr, spec: testSpec("3000"), hit: make(chan struct{}, 1)}
	e.hit <- struct{}{} // the late hit of a previous attempt

	err := e.Verify(time.Second)
	if err == nil {
		t.Fatal("Verify passed on a token from an earlier attempt, without its own nonce ever coming back")
	}
	if !strings.Contains(err.Error(), "answered by something else") {
		t.Fatalf("unexpected verdict: %v", err)
	}
}

// The two dial failures a user can act on, told apart. A refusal means the name
// resolves and the port does not answer (an agent image predating -s bound the
// loopback); anything else means the name is not declared at all.
func TestVerifyExplainsWhyTheClusterDialFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dial  error
		wants []string
	}{
		{"refused", errors.New("dial tcp 10.0.1.4:80: connect: connection refused"),
			[]string{"refuses the connection", "update the agent image"}},
		{"no such name", errors.New("ssh: rejected: administratively prohibited (open failed)"),
			[]string{"not reachable inside the cluster", "declare the name on the agent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeExposeTransport{dial: func(string, time.Duration) (net.Conn, error) { return nil, tc.dial }}
			e := &Exposed{t: tr, spec: testSpec("3000"), hit: make(chan struct{}, 1)}
			err := e.Verify(time.Second)
			if err == nil {
				t.Fatal("a failed cluster dial was reported as a verified path")
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("the verdict does not say %q: %v", w, err)
				}
			}
		})
	}
}
