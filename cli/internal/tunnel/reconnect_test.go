package tunnel

// What happens when the network drops in the middle of a session.
//
// The transport's promise is that it self-heals: a dead-but-open connection is
// noticed by the keepalive, closed, and replaced, and everything riding it is
// torn down so the -s serve loops re-arm. Until this file, none of that was
// tested. dropDead had no test at all, reconnectFrom was only ever reached by
// the very first Dial, and the keepalive loop was never run: the suite stayed
// green with dropDead turned into a no-op AND reconnectFrom refusing to re-dial
// a dead client, which is the whole self-healing feature deleted.
//
// The failures these tests exist to keep out, in the shape the user meets them:
//   - the laptop wakes up and plug is a zombie: the socket is ESTABLISHED,
//     nothing crosses it, and no reconnect ever happens;
//   - a single lost ping tears down a healthy connection, killing every live
//     channel on it for nothing;
//   - a reconnect storm: N goroutines finding the same dead client and each
//     dialing its own replacement;
//   - a re-dial that fails during an outage leaves the zombie in place, and the
//     keepalive spends the outage pinging it, leaking a goroutine per tick.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

// holdingAgent is an in-process SSH agent that ACCEPTS every direct-tcpip channel
// and keeps it open, and that says out loud when any channel is opened.
//
// rejectingServer (reject_test.go) is reused where the question is "the agent
// bounced this channel". It cannot answer the questions here, for two reasons.
// Watching what becomes of a connection IN FLIGHT during a replacement needs a
// channel that is actually alive, and rejectingServer bounces them all. And
// driving the keepalive by hand means the test has to know when the transport's
// own background goroutines are done: every successful dial spawns
// checkAgentVersion, and a test that starts breaking things while that is still
// in flight is asserting about two schedulers at once.
type holdingAgent struct {
	addr   string
	conns  *atomic.Int32 // TCP connections accepted, ever
	opened chan string   // channel type, once per channel the agent was asked for
	ln     net.Listener
}

func newHoldingAgent(t *testing.T) *holdingAgent {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &holdingAgent{addr: ln.Addr().String(), conns: &atomic.Int32{}, opened: make(chan string, 64), ln: ln}
	t.Cleanup(a.stop)
	go a.serve(cfg)
	return a
}

// stop takes the agent off the air without touching the connections it has
// already accepted: exactly the shape of a cluster that goes away mid-session,
// where the socket in hand is dead and a re-dial is refused.
func (a *holdingAgent) stop() { a.ln.Close() }

func (a *holdingAgent) serve(cfg *ssh.ServerConfig) {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			return
		}
		a.conns.Add(1)
		go func(c net.Conn) {
			sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
			if err != nil {
				c.Close()
				return
			}
			defer sconn.Close()
			go ssh.DiscardRequests(reqs)
			for nc := range chans {
				a.saw(nc.ChannelType())
				if nc.ChannelType() != "direct-tcpip" {
					nc.Reject(ssh.Prohibited, "this agent only forwards")
					continue
				}
				ch, creqs, err := nc.Accept()
				if err != nil {
					continue
				}
				go ssh.DiscardRequests(creqs)
				go io.Copy(io.Discard, ch) // hold it open until the connection dies under it
			}
		}(c)
	}
}

func (a *holdingAgent) saw(kind string) {
	select {
	case a.opened <- kind:
	default: // a test that stopped reading must never wedge the agent
	}
}

// waitOpen blocks until the agent was asked for a channel of this kind. That is
// the test's happens-before edge over whichever goroutine asked for it.
func (a *holdingAgent) waitOpen(t *testing.T, kind string) {
	t.Helper()
	giveUp := time.After(15 * time.Second) // bounds a failure, never awaited on success
	for {
		select {
		case got := <-a.opened:
			if got == kind {
				return
			}
		case <-giveUp:
			t.Fatalf("the agent was never asked for a %q channel", kind)
		}
	}
}

// noteRecorder captures what the transport tells the user. During an outage that
// notice is the only thing standing between a stuck session and a bug report.
type noteRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (n *noteRecorder) logf(format string, a ...any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, fmt.Sprintf(format, a...))
}

func (n *noteRecorder) all() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.msgs, "\n")
}

// connectedTransport is Dial without the keepalive goroutine: in this file the
// TEST is the keepalive, driving keepaliveOn tick by tick, and a second loop
// running off the real 15s clock is the nondeterminism this file exists to avoid.
// Everything else is what Dial does.
func connectedTransport(t *testing.T, a *holdingAgent) (*Transport, *noteRecorder) {
	t.Helper()
	host, port, err := net.SplitHostPort(a.addr)
	if err != nil {
		t.Fatal(err)
	}
	notes := &noteRecorder{}
	tr := &Transport{
		host: host, port: port, user: "plug",
		keys: [][]byte{clientKeyPEM(t)},
		logf: notes.logf,
		done: make(chan struct{}),
	}
	if _, err := tr.reconnectFrom(nil); err != nil {
		t.Fatalf("connecting to the test agent: %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	// Every successful dial spawns checkAgentVersion, which asks the agent for a
	// session. Wait until that has actually reached the agent, so each test starts
	// from a settled transport: from here on the only goroutine touching it is the
	// keepalive the test drives itself, and every count below is about what the
	// test did.
	a.waitOpen(t, "session")
	return tr, notes
}

// keepaliveDriver runs keepaliveOn with the test holding both of its clocks.
// Ticks travel over an UNBUFFERED channel, and that is the entire synchronisation
// scheme: the loop only takes a tick when it is back at its select, so a tick
// that is ACCEPTED proves the previous tick's work is finished, reconnect
// included. Nothing here is decided by a sleep.
type keepaliveDriver struct {
	tick    chan time.Time
	replies chan bool // one scripted verdict per expected ping
	pings   atomic.Int32
	extra   atomic.Int32 // pings the script did not expect
}

func driveKeepalive(tr *Transport, verdicts ...bool) *keepaliveDriver {
	d := &keepaliveDriver{tick: make(chan time.Time), replies: make(chan bool, len(verdicts))}
	for _, v := range verdicts {
		d.replies <- v
	}
	go tr.keepaliveOn(d.tick, d.ping)
	return d
}

func (d *keepaliveDriver) ping(*ssh.Client) bool {
	d.pings.Add(1)
	select {
	case v := <-d.replies:
		return v
	default:
		// A ping the script did not order is a finding, not a reason to deadlock:
		// count it, answer harmlessly, and let the assertions name it.
		d.extra.Add(1)
		return true
	}
}

// beat delivers one tick, and returns once the loop has taken it - which is once
// the PREVIOUS tick has been handled to the end.
func (d *keepaliveDriver) beat(t *testing.T) {
	t.Helper()
	select {
	case d.tick <- time.Now():
	case <-time.After(15 * time.Second):
		t.Fatal("the keepalive loop never came back for another tick")
	}
}

// closedWhen reports when an SSH connection really goes down. Wait returns when
// the transport is torn down, which is the only honest way to ask whether the
// zombie was CLOSED: a cleared field would merely say it was forgotten, and a
// forgotten connection still holds every channel and listener on it.
func closedWhen(cl *ssh.Client) <-chan struct{} {
	down := make(chan struct{})
	go func() { cl.Wait(); close(down) }()
	return down
}

func mustGoDown(t *testing.T, down <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-down:
	case <-time.After(15 * time.Second): // bounds a failure, never awaited on success
		t.Fatalf("%s was never closed: everything riding it stays wedged", what)
	}
}

// dialThrough opens one channel through the tunnel, which is what any forwarded
// flow does.
func dialThrough(t *testing.T, tr *Transport) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return tr.DialContext(ctx, "tcp", "svc.cluster:80")
}

// The miss counts below are literals on purpose. Deriving them from
// keepaliveMisses would make these tests agree with whatever the constant is
// changed to, and that number IS the behaviour under test: at 1 a hiccup kills
// every live channel, at 3 or more a woken laptop stays a zombie for another
// cadence. Changing it is a decision, and it should turn these red.

// Two consecutive missed pings mean the path is dead: the connection is closed,
// cleared and replaced by ONE new one. Then the counter starts over, so the miss
// that follows a fresh reconnect is a first miss again and costs nothing.
//
// The failure this keeps out is the original zombie: after a laptop sleep the
// socket stays ESTABLISHED, every ping hangs, and without the drop-and-re-dial
// the session is dead until the user restarts plug. The "and only once" half
// keeps out its opposite: a counter that is not reset reconnects on every tick
// for as long as the pings keep failing.
func TestKeepaliveReplacesTheDeadConnectionOnceAndThenStartsCounting(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	dead := tr.current()
	if dead == nil {
		t.Fatal("no connection after the initial dial")
	}
	if got := agent.conns.Load(); got != 1 {
		t.Fatalf("the initial dial made %d connections, want 1", got)
	}
	down := closedWhen(dead)

	// miss, miss (the path is declared dead here), miss (a FIRST miss again on the
	// replacement), then one settling tick that proves the third was handled.
	d := driveKeepalive(tr, false, false, false, true)
	for i := 0; i < 4; i++ {
		d.beat(t)
	}

	mustGoDown(t, down, "the connection the keepalive declared dead")
	if got := agent.conns.Load(); got != 2 {
		t.Fatalf("the agent saw %d connections, want 2: one dead path must cost exactly one re-dial", got)
	}
	live := tr.current()
	if live == nil {
		t.Fatal("the transport was left with no connection at all: every later dial fails")
	}
	if live == dead {
		t.Fatal("the transport is still handing out the connection it just declared dead")
	}
	if extra := d.extra.Load(); extra != 0 {
		t.Fatalf("%d unscripted pings: the loop is not pinging once per tick", extra)
	}

	// And the replacement carries traffic, over that same second connection: a
	// reconnect that leaves the datapath dialing again per flow is not a reconnect.
	conn, err := dialThrough(t, tr)
	if err != nil {
		t.Fatalf("the replacement connection carries nothing: %v", err)
	}
	conn.Close()
	if got := agent.conns.Load(); got != 2 {
		t.Fatalf("the agent saw %d connections after one flow, want 2", got)
	}
}

// One missed ping is a blip, not an outage. Reconnecting on it would close the
// shared connection and take down every channel and every -s listener riding it,
// on a link that was merely slow for a moment. The counter must also reset on a
// good ping, or two blips a minute apart would add up to a teardown.
func TestAnIsolatedMissNeverTouchesTheConnection(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	first := tr.current()

	// miss, ok, miss, ok: two blips that never follow each other, plus a settling
	// tick to prove the fourth was handled.
	d := driveKeepalive(tr, false, true, false, true, true)
	for i := 0; i < 5; i++ {
		d.beat(t)
	}

	if got := agent.conns.Load(); got != 1 {
		t.Fatalf("the agent saw %d connections, want 1: a single missed ping must not reconnect", got)
	}
	if tr.current() != first {
		t.Fatal("the connection was replaced over blips that never followed each other")
	}
	if extra := d.extra.Load(); extra != 0 {
		t.Fatalf("%d unscripted pings: the loop is not pinging once per tick", extra)
	}
	// Still alive, not just still referenced.
	conn, err := dialThrough(t, tr)
	if err != nil {
		t.Fatalf("the connection no longer carries traffic after two tolerated blips: %v", err)
	}
	conn.Close()
	if got := agent.conns.Load(); got != 1 {
		t.Fatalf("the flow rode a %dth connection, so the first one was not usable after all", got)
	}
}

// Every goroutine that finds the same dead client asks for a replacement, and
// they arrive together: the keepalive, the datapath retry in DialContext, Exec,
// each -s serve loop re-arming. They must share ONE new connection. Without the
// dedup they each dial their own, and an outage ends in a burst of connections
// where all but one are immediately orphaned.
func TestConcurrentReconnectsFromOneDeadClientDialOnce(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	dead := tr.current()
	down := closedWhen(dead)

	const racers = 8
	got := make([]*ssh.Client, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i], errs[i] = tr.reconnectFrom(dead)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d failed to reconnect: %v", i, err)
		}
	}
	if n := agent.conns.Load(); n != 2 {
		t.Fatalf("%d goroutines finding one dead client opened %d connections in total, want 2 "+
			"(the original and a single replacement)", racers, n)
	}
	for i, c := range got {
		if c == nil || c == dead {
			t.Fatalf("racer %d was handed %v, which is the dead client or nothing", i, c)
		}
		if c != got[0] {
			t.Fatalf("racer %d got a different connection than racer 0: the transport handed out two", i)
		}
	}
	if tr.current() != got[0] {
		t.Fatal("the transport kept a connection none of the callers is using")
	}
	// The client that was replaced is closed, not leaked: it is the only chance to
	// free it, and everything still waiting on it must be told.
	mustGoDown(t, down, "the client reconnectFrom replaced")
}

// dropDead clears the transport's client only when it is still the one that was
// declared dead. A keepalive that wakes up holding an old handle - the ping it
// was blocked on finally returning, a slow scheduler - must not wipe the healthy
// connection that replaced it: that would leave a working transport pointing at
// nothing and the next flow re-dialing for no reason.
func TestDropDeadClearsOnlyTheConnectionItWasGiven(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	first := tr.current()
	down := closedWhen(first)

	tr.dropDead(first)
	if tr.current() != nil {
		t.Fatal("the dead connection is still the one callers are handed")
	}
	mustGoDown(t, down, "the connection dropDead was given")

	second, err := tr.reconnectFrom(nil)
	if err != nil {
		t.Fatalf("reconnecting after the drop: %v", err)
	}
	tr.dropDead(first) // the late keepalive, still holding the old handle
	if tr.current() != second {
		t.Fatal("a late drop of an already-replaced connection wiped the live one")
	}
	tr.dropDead(nil) // nothing to close, and nothing to panic about
	if tr.current() != second {
		t.Fatal("dropping nothing dropped the live connection")
	}
}

// When the re-dial fails - the cluster is gone, the VPN is down - the transport
// must be left EMPTY, not holding the zombie. The next tick then sees nothing to
// ping and waits quietly. Keeping the zombie means pinging it every cadence for
// the length of the outage, and since a dead ping never returns, each of those
// leaks a goroutine that is only released if the connection is ever closed.
func TestAFailedRedialLeavesNothingToPing(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, notes := connectedTransport(t, agent)
	first := tr.current()
	down := closedWhen(first)
	agent.stop() // the cluster goes away; the socket we hold is now a zombie

	// miss, miss (dead: drop, then a re-dial that cannot succeed), then two ticks
	// that must find nothing to ping - the second proves the first was handled.
	d := driveKeepalive(tr, false, false)
	for i := 0; i < 4; i++ {
		d.beat(t)
	}

	mustGoDown(t, down, "the zombie connection")
	if tr.current() != nil {
		t.Fatal("a failed re-dial left the zombie in place: callers keep being handed a dead connection")
	}
	if got := d.pings.Load(); got != 2 {
		t.Fatalf("the loop sent %d pings, want 2: with no connection there is nothing to ping, "+
			"and every ping of a zombie leaks the goroutine that sent it", got)
	}
	if !strings.Contains(notes.all(), "unreachable") {
		t.Fatalf("the outage was never reported to the user, notes were:\n%s", notes.all())
	}
}

// A flow that is IN FLIGHT when the connection is replaced must die with it,
// promptly. That teardown is the mechanism the -s forwards rely on: their
// Accept() unblocks when the stale client closes, which is what re-arms them on
// the new connection. Leave the old channels open and the caller waits on bytes
// that can never arrive, on a connection nobody is servicing any more.
func TestAFlowInFlightDiesWithTheReplacedConnection(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	first := tr.current()

	inflight, err := dialThrough(t, tr)
	if err != nil {
		t.Fatalf("opening the in-flight flow: %v", err)
	}
	agent.waitOpen(t, "direct-tcpip") // it is really established, not merely requested

	read := make(chan error, 1)
	go func() {
		_, rerr := inflight.Read(make([]byte, 1))
		read <- rerr
	}()

	d := driveKeepalive(tr, false, false, true)
	for i := 0; i < 3; i++ {
		d.beat(t)
	}

	select {
	case rerr := <-read:
		if rerr == nil {
			t.Fatal("the in-flight flow returned data from a connection that was torn down")
		}
	case <-time.After(15 * time.Second): // bounds a failure, never awaited on success
		t.Fatal("the in-flight flow is still waiting on a connection that was replaced: " +
			"nothing riding the old client was told, so no serve loop re-arms")
	}
	inflight.Close()

	if got := agent.conns.Load(); got != 2 {
		t.Fatalf("the agent saw %d connections, want 2", got)
	}
	next, err := dialThrough(t, tr)
	if err != nil {
		t.Fatalf("the flow after the reconnect could not be opened: %v", err)
	}
	next.Close()
	if got := agent.conns.Load(); got != 2 {
		t.Fatalf("the flow after the reconnect opened its own connection (%d total): "+
			"it did not ride the replacement", got)
	}
	if first == tr.current() {
		t.Fatal("the transport is still on the connection it declared dead")
	}
}

// Close is final. Anything that reconnects on demand - the keepalive, the
// datapath retry, a serve loop - runs concurrently with the user's teardown, and
// a re-dial that slips through would resurrect a transport that was asked to go
// away: a connection to the cluster nobody closes, held by a session that is
// over.
func TestAClosedTransportNeverDialsAgain(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tr.reconnectFrom(nil); !errors.Is(err, errClosed) {
		t.Fatalf("reconnectFrom after Close returned %v, want %v", err, errClosed)
	}
	if _, err := dialThrough(t, tr); err == nil {
		t.Fatal("a closed transport still opened a flow")
	}
	if got := agent.conns.Load(); got != 1 {
		t.Fatalf("the agent saw %d connections, want 1: a closed transport dialed again", got)
	}
}
