// Package tunnel implements the plug data path: an SSH transport to the agent
// (this file). Every outbound flow (and DNS, over TCP) becomes an SSH
// direct-tcpip channel, so sshd itself opens the real connection from inside
// the cluster — no server code of ours, and no sshuttle/Python.
//
// The transport self-heals: a keepalive keeps the single SSH connection warm
// and detects its death fast, and a dead connection is transparently re-dialed
// on the next use (and proactively by the keepalive). So an idle NAT/VPN/LB
// drop no longer requires restarting plug.
package tunnel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// clusterResolver is the Docker/Swarm embedded DNS, reachable over TCP from
// inside the agent.
const clusterResolver = "127.0.0.11:53"

const (
	keepaliveEvery   = 15 * time.Second // ping cadence: keep NAT/LB/VPN paths warm, detect death
	keepaliveTimeout = 8 * time.Second  // a reply slower than this means the path is DEAD, not slow
	keepaliveMisses  = 2                // consecutive misses before reconnecting (tolerate one blip)
	dialTimeout      = 15 * time.Second // initial TCP+SSH handshake to the agent
	channelTimeout   = 10 * time.Second // bound a single direct-tcpip channel open
)

// Logf is where the data paths report progress; set by the caller.
type Logf func(format string, a ...any)

var errClosed = errors.New("tunnel: transport closed")

// relay copies bidirectionally. On EOF in one direction it half-closes that
// write side (CloseWrite) so the peer can still drain the other direction,
// then closes both once both directions finish — preserving protocols that
// shut down one half and keep reading.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// Transport is a self-healing SSH connection to the agent, used as a demux for
// outbound cluster traffic. It is safe for concurrent use: ssh.Client
// multiplexes channels over the one connection, and the reconnect path is
// mutex-guarded.
type Transport struct {
	host, port, user string
	keys             [][]byte
	knownHosts       string
	logf             Logf

	mu     sync.Mutex
	client *ssh.Client
	closed bool
	done   chan struct{}

	// resolveUnsupported remembers an agent that predates the `resolve` verb
	// (it answered "unknown command") — asked once, then every later name
	// check skips the round-trip and falls back to minting.
	resolveUnsupported atomic.Bool

	// agentVer is the version the agent reported when this transport last
	// looked. An update to the agent restarts it, every session reconnects by
	// itself, and the version it answers afterwards is the new one — comparing
	// the two is how a running session finds out it is now the older side.
	agentVer atomic.Value // string
}

// Dial opens the SSH transport to the agent as the tunnel user. keys are the
// private keys to offer, in order: a profile's personal key first when it has
// one, then the key built into the binary. SSH tries them in turn and the agent
// picks the one it knows, so generating a personal key never cuts you off from a
// cluster that does not do key authentication yet.
//
// The agent host key is pinned on first use (TOFU) in knownHostsPath and
// verified on every later connect; pass "" to skip pinning. logf (may be nil)
// receives reconnect/keepalive notices.
func Dial(host, port, user string, keys [][]byte, knownHostsPath string, logf Logf) (*Transport, error) {
	t := &Transport{
		host: host, port: port, user: user, keys: keys,
		knownHosts: knownHostsPath, logf: logf,
		done: make(chan struct{}),
	}
	if _, err := t.reconnectFrom(nil); err != nil {
		return nil, err
	}
	go t.keepalive()
	return t, nil
}

func (t *Transport) note(format string, a ...any) {
	if t.logf != nil {
		t.logf(format, a...)
	}
}

// dial establishes a fresh SSH client to the agent.
func (t *Transport) dial() (*ssh.Client, error) {
	// Every key must parse. A key that was configured on purpose and cannot be
	// read is a mistake worth naming, not something to skip quietly: dropping it
	// would authenticate as somebody else (the embedded key) and the human would
	// never learn their own key is unusable.
	signers := make([]ssh.Signer, 0, len(t.keys))
	for i, k := range t.keys {
		signer, err := ssh.ParsePrivateKey(k)
		if err != nil {
			return nil, fmt.Errorf("parsing private key %d of %d: %w", i+1, len(t.keys), err)
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		return nil, fmt.Errorf("no private key to authenticate with")
	}
	addr := net.JoinHostPort(t.host, t.port)
	cfg := &ssh.ClientConfig{
		User:            t.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: tofuHostKey(t.knownHosts, addr, t.note),
		Timeout:         dialTimeout,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		if isAuthFailure(err) {
			return nil, &AuthFailure{Addr: addr, Err: err, Offered: fingerprints(signers)}
		}
		return nil, fmt.Errorf("ssh to %s: %w", addr, err)
	}
	return client, nil
}

// AuthFailure is the agent refusing every key we hold. It is separated from
// every other dial error because it is the only one that will not fix itself:
// a refused key stays refused until somebody enrols it, so retrying is not
// resilience, it is noise that buries the reason.
type AuthFailure struct {
	Addr    string
	Err     error
	Offered []string // fingerprints, in the order they were presented
}

func (e *AuthFailure) Error() string {
	return fmt.Sprintf("%s refused every key offered (%s)", e.Addr, strings.Join(e.Offered, ", "))
}

func (e *AuthFailure) Unwrap() error { return e.Err }

// IsAuthFailure reports whether err is an agent refusing our keys.
func IsAuthFailure(err error) bool {
	var af *AuthFailure
	return errors.As(err, &af)
}

// isAuthFailure recognises what x/crypto/ssh says when no method is left. There
// is no typed error to match on: the package returns a plain error whose text is
// "ssh: unable to authenticate, attempted methods […], no supported methods
// remain". Matched on the stable half of that sentence, and wrapped into a type
// so nothing else in plug has to know this.
func isAuthFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

// fingerprints names the keys actually presented, so a refusal can be matched
// against what `plug pubkey` prints.
func fingerprints(signers []ssh.Signer) []string {
	out := make([]string, 0, len(signers))
	for _, s := range signers {
		out = append(out, ssh.FingerprintSHA256(s.PublicKey()))
	}
	return out
}

// current returns the live client (nil only before the first successful dial).
func (t *Transport) current() *ssh.Client {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.client
}

// reconnectFrom swaps in a fresh client, unless another goroutine already
// replaced `stale` (the client the caller found dead). Returns the client to
// use next.
func (t *Transport) reconnectFrom(stale *ssh.Client) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errClosed
	}
	if t.client != nil && t.client != stale {
		return t.client, nil // someone else already reconnected
	}
	nc, err := t.dial()
	if err != nil {
		return nil, err
	}
	if stale != nil {
		stale.Close()
		t.note("agent connection re-established")
	}
	t.client = nc
	go t.checkAgentVersion()
	return nc, nil
}

// checkAgentVersion asks the agent what version it is now and says so when that
// differs from what it answered before. The running core cannot become the new
// version — the launcher picks it once, before exec, and this process is holding
// the user's command at the end of its pipes — so the only useful thing to do
// with the news is to say it.
//
// Runs in its own goroutine, out of the reconnect path: the caller holds t.mu,
// which Exec needs, and a reconnect must not wait on a round-trip to report a
// version nobody is blocked on.
func (t *Transport) checkAgentVersion() {
	now, err := t.Exec("version")
	if err != nil || now == "" || strings.HasPrefix(now, "error:") {
		return // an agent too old to answer, or a connection already gone again
	}
	was, _ := t.agentVer.Load().(string)
	t.agentVer.Store(now)
	if was != "" && was != now {
		t.note("the agent moved from v%s to v%s — this session keeps running the older core, "+
			"restart it to pick the new one", was, now)
	}
}

// dropDead closes a client the keepalive confirmed dead and clears it if it is
// still the current one. Closing unblocks the hung SendRequest and errors every
// channel/listener riding it (so the -s serve loops re-arm); clearing it means a
// failed re-dial won't leave callers using the zombie.
func (t *Transport) dropDead(cl *ssh.Client) {
	t.mu.Lock()
	if t.client == cl {
		t.client = nil
	}
	t.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
}

// DialContext opens a connection to addr from inside the cluster via a
// direct-tcpip channel. A dead transport is re-dialed once and the open
// retried, so an idle-dropped connection self-heals without restarting plug.
func (t *Transport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("tunnel: unsupported network %q", network)
	}
	cl := t.current()
	if cl == nil {
		var err error
		if cl, err = t.reconnectFrom(nil); err != nil {
			return nil, err
		}
	}
	conn, err := channelDial(ctx, cl, addr)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err() // the caller gave up — not a dead transport
	}
	// A channel REJECTED by the agent (the cluster name doesn't resolve, the port is
	// refused…) means the SSH connection is HEALTHY. Reconnecting on it is not only
	// pointless — it closes the shared connection and tears down every other channel
	// riding it. That turned Windows' constant WPAD/mDNS probes (bare names the agent
	// rejects) into cascading reconnects that killed concurrent `plug` sessions. Surface
	// a rejection as-is, without touching the connection.
	var oce *ssh.OpenChannelError
	if errors.As(err, &oce) {
		return nil, err
	}
	// Otherwise the connection may be dead. Reconnect once and retry.
	cl2, rerr := t.reconnectFrom(cl)
	if rerr != nil {
		return nil, err // surface the original open error
	}
	return channelDial(ctx, cl2, addr)
}

// channelDial opens the direct-tcpip channel, with ctx bounding the open so a
// half-dead tunnel fails fast instead of blocking on kernel TCP timeouts.
func channelDial(ctx context.Context, cl *ssh.Client, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := cl.Dial("tcp", addr)
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		// cl.Dial may still succeed AFTER we give up — reap that late channel, or
		// every timed-out dial leaks an open direct-tcpip channel on the shared SSH
		// connection. During an outage those pile up and can starve the transport
		// for good: dials then keep timing out even after the service comes back.
		go func() {
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

// DialCluster dials a cluster service:port through the tunnel, bounding the
// channel open with a timeout.
func (t *Transport) DialCluster(addr string) (net.Conn, error) {
	return t.DialClusterTimeout(addr, channelTimeout)
}

// DialClusterTimeout is DialCluster with the open bounded by d instead of the
// datapath default. For probing a name that may not be up YET: a service whose
// backend isn't ready drops the SYN rather than refusing it (a Swarm VIP with
// no running task, a k8s Service with no endpoint), so an early probe cannot
// fail fast — it costs the full timeout to learn nothing. A short budget,
// retried, finds the moment the path opens instead of paying channelTimeout to
// be told it hadn't opened yet. The datapath keeps the long one: there, a slow
// open is a slow service, not a race with provisioning.
func (t *Transport) DialClusterTimeout(addr string, d time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return t.DialContext(ctx, "tcp", addr)
}

// keepalive keeps the single SSH connection warm and, crucially, detects a
// DEAD-BUT-OPEN connection. After a laptop sleep — or a Docker Desktop VM that
// suspends the agent behind its localhost:2222 proxy — the socket can stay
// ESTABLISHED while nothing crosses it end to end. A plain SendRequest then
// BLOCKS FOREVER on a reply that never comes, wedging the keepalive and leaving
// the tunnel a zombie (the bug this fixes). So each ping is bounded by
// keepaliveTimeout; a hung reply is a miss, and keepaliveMisses in a row
// triggers a reconnect — which re-dials the upward path AND re-arms every -s
// forward riding this transport (their Accept() unblocks when the stale client
// closes). One miss is tolerated so a brief blip causes no needless reconnect.
// PLUG_KEEPALIVE_SECS overrides the cadence for the rare exotic link.
func (t *Transport) keepalive() {
	every := keepaliveEvery
	if s := os.Getenv("PLUG_KEEPALIVE_SECS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			every = time.Duration(n) * time.Second
		}
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	misses := 0
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			cl := t.current()
			if cl == nil {
				misses = 0
				continue
			}
			if pingOK(cl, keepaliveTimeout) {
				misses = 0
				continue
			}
			if misses++; misses < keepaliveMisses {
				continue // tolerate one transient miss
			}
			misses = 0
			// Confirmed dead: close the zombie NOW (unblocks the hung ping and
			// every channel/listener on it) and clear it — so if the re-dial
			// below fails, the next tick sees a nil client and stops pinging the
			// zombie, instead of leaking a goroutine every tick for the whole
			// outage. Then re-dial fresh.
			t.dropDead(cl)
			if _, rerr := t.reconnectFrom(nil); rerr != nil {
				t.note("keepalive: agent unreachable (%v)", rerr)
			}
		}
	}
}

// pinger is the slice of *ssh.Client keepalive needs — an interface so the
// timeout logic is unit-testable without a live sshd.
type pinger interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

// pingOK sends one keepalive and waits at most timeout for the reply. The
// SendRequest runs in its own goroutine: x/crypto/ssh gives no way to cancel it,
// so on a zombie connection it blocks until the connection is finally closed
// (the reconnect does that). The buffered channel lets that goroutine deliver
// its late result and exit without leaking.
func pingOK(p pinger, timeout time.Duration) bool {
	res := make(chan error, 1)
	go func() {
		_, _, err := p.SendRequest("keepalive@openssh.com", true, nil)
		res <- err
	}()
	select {
	case err := <-res:
		return err == nil
	case <-time.After(timeout):
		return false // reply hung — the path is dead
	}
}

// Resolver returns a *net.Resolver that performs lookups against the cluster's
// embedded DNS over the tunnel (DNS over TCP), exactly as a container would.
func (t *Transport) Resolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Force TCP: sshd direct-tcpip is stream-only, and the embedded
			// resolver answers DNS over TCP on the same address.
			return t.DialContext(ctx, "tcp", clusterResolver)
		},
	}
}

// Exec runs one agent-side command over an SSH session and returns the first
// line of its combined output. Used by the -s provisioning verbs (serve-name /
// unserve-name): the agent's ForceCommand answers the one-line protocol; an
// OLD agent image runs the command through /bin/sh instead and answers
// "sh: serve-name: not found" — anything off-protocol is an agent that cannot
// serve the verb, and the caller says so rather than guessing.
func (t *Transport) Exec(cmd string) (string, error) {
	cl := t.current()
	if cl == nil {
		var err error
		if cl, err = t.reconnectFrom(nil); err != nil {
			return "", err
		}
	}
	s, err := cl.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, cerr := s.CombinedOutput(cmd)
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	// The agent answers its own errors as an "error: …" line and exits non-zero,
	// so a non-empty line IS the answer — cerr adds nothing. But an empty line
	// with an error means the session died before the agent said anything (a
	// teardown over a network that is already gone, typically): reporting that
	// as ("", nil) makes a command that never ran indistinguishable from one
	// that succeeded, and the caller then skips the warning it exists to print.
	if cerr != nil && line == "" {
		return "", cerr
	}
	return line, nil
}

// Close tears down the transport (and every channel on it) and stops the
// keepalive.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cl := t.client
	t.client = nil
	close(t.done)
	t.mu.Unlock()
	if cl != nil {
		return cl.Close()
	}
	return nil
}

// tofuHostKey returns a host-key callback that pins the agent's key on first
// sight (trust on first use) in path and verifies it on every later connect —
// a cheap MITM tripwire on top of plug's no-secret transport. With path == ""
// it accepts any key (the previous behaviour).
// HostKeyCallback is tofuHostKey for callers outside the transport: the DOWNLOAD
// channel, which carries the version, the digest and the binary that is then run
// with privilege, and which pinned nothing at all. Same policy as the tunnel, on
// the same file, so one agent is recorded once and a change is noticed wherever
// it shows up first.
func HostKeyCallback(path, addr string, note Logf) ssh.HostKeyCallback {
	return tofuHostKey(path, addr, note)
}

func tofuHostKey(path, addr string, note func(string, ...any)) ssh.HostKeyCallback {
	if path == "" {
		return ssh.InsecureIgnoreHostKey()
	}
	if note == nil {
		// Logf is documented as optional everywhere else in this package, and this
		// callback calls it on both of its interesting paths - first sight, and a
		// key that changed. A nil one panicked INSIDE the handshake, where the
		// failure reads as a broken connection rather than a missing argument.
		// Found by exporting the callback and passing nil from the first caller
		// that could.
		note = func(string, ...any) {}
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		enc := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
		data, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.SplitN(strings.TrimSpace(line), " ", 2)
			if len(f) == 2 && f[0] == addr {
				if f[1] == enc {
					return nil // known and matches
				}
				// The agent regenerates its host key on EVERY start (ssh-keygen -A),
				// so a changed key is the NORMAL case after a cluster/agent restart,
				// not an attack — plug's model is a trusted dev cluster, and the
				// install already connects with StrictHostKeyChecking=no. Blocking
				// here just forced the user to hand-edit known_hosts after each
				// restart, which trained them to ignore the warning. Re-pin instead
				// and note it: the note is the informative tripwire (a key change on a
				// host you did NOT restart still deserves a glance), without the chore.
				note("agent host key for %s changed (agent restart?) — re-pinned", addr)
				repin(path, addr, enc)
				return nil
			}
		}
		// First sight — pin it.
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s %s\n", addr, enc)
			f.Close()
		}
		note("pinned agent host key (%s)", ssh.FingerprintSHA256(key))
		return nil
	}
}

// repin rewrites path with enc as the pinned key for addr, dropping any stale
// line for that addr (and blank lines). Best-effort: a failed rewrite just means
// the next connect re-pins again.
func repin(path, addr, enc string) {
	data, _ := os.ReadFile(path)
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if f := strings.SplitN(t, " ", 2); len(f) == 2 && f[0] == addr {
			continue // drop the old pin for this addr
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	b.WriteString(addr + " " + enc + "\n")
	_ = os.WriteFile(path, []byte(b.String()), 0o600)
}

// ResolveInCluster reports whether name exists in this transport's cluster,
// asked THROUGH the agent (its resolver is where that truth lives): the CLI's
// resolver calls this before minting a fake IP for a bare name, so an absent
// name gets an honest NXDOMAIN instead of a fake that can only ever refuse the
// connect. ok=false means the answer is unusable — an agent that predates the
// verb — and the caller should mint as before (the degradation contract).
// Transport hiccups fail OPEN (found=true, ok=true): a reconnecting tunnel
// must never break name resolution.
func (t *Transport) ResolveInCluster(name string) (found, ok bool) {
	if t.resolveUnsupported.Load() {
		return true, false
	}
	cl := t.current()
	if cl == nil {
		return true, true
	}
	sess, err := cl.NewSession()
	if err != nil {
		return true, true
	}
	defer sess.Close()
	type res struct {
		out []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, oerr := sess.Output("resolve " + name)
		ch <- res{out, oerr}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(3 * time.Second):
		return true, true // a wedged session must not stall DNS — fail open
	}
	switch ans := strings.TrimSpace(string(r.out)); {
	case ans == "found":
		return true, true
	case ans == "nxdomain":
		return false, true
	case strings.Contains(ans, "unknown command"):
		t.resolveUnsupported.Store(true) // pre-2.2 agent — remember, mint as before
		return true, false
	default:
		return true, true // garbled/error — fail open, retry next time
	}
}
