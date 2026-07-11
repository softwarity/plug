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
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// clusterResolver is the Docker/Swarm embedded DNS, reachable over TCP from
// inside the agent.
const clusterResolver = "127.0.0.11:53"

const (
	keepaliveEvery = 30 * time.Second // keep NAT/LB/VPN paths warm; detect death
	dialTimeout    = 15 * time.Second // initial TCP+SSH handshake to the agent
	channelTimeout = 10 * time.Second // bound a single direct-tcpip channel open
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
	key              []byte
	knownHosts       string
	logf             Logf

	mu     sync.Mutex
	client *ssh.Client
	closed bool
	done   chan struct{}
}

// Dial opens the SSH transport to the agent as the tunnel user, authenticating
// with the embedded private key. The agent host key is pinned on first use
// (TOFU) in knownHostsPath and verified on every later connect; pass "" to skip
// pinning. logf (may be nil) receives reconnect/keepalive notices.
func Dial(host, port, user string, privateKey []byte, knownHostsPath string, logf Logf) (*Transport, error) {
	t := &Transport{
		host: host, port: port, user: user, key: privateKey,
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
	signer, err := ssh.ParsePrivateKey(t.key)
	if err != nil {
		return nil, fmt.Errorf("parsing embedded key: %w", err)
	}
	addr := net.JoinHostPort(t.host, t.port)
	cfg := &ssh.ClientConfig{
		User:            t.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: tofuHostKey(t.knownHosts, addr, t.note),
		Timeout:         dialTimeout,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh to %s: %w", addr, err)
	}
	return client, nil
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
	return nc, nil
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
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

// DialCluster dials a cluster service:port through the tunnel, bounding the
// channel open with a timeout.
func (t *Transport) DialCluster(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), channelTimeout)
	defer cancel()
	return t.DialContext(ctx, "tcp", addr)
}

// keepalive keeps the single SSH connection warm (so idle NAT/LB/VPN paths do
// not silently drop it) and detects a dead connection quickly, reconnecting
// proactively so the next request is instant.
func (t *Transport) keepalive() {
	tk := time.NewTicker(keepaliveEvery)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			cl := t.current()
			if cl == nil {
				continue
			}
			if _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				if _, rerr := t.reconnectFrom(cl); rerr != nil {
					t.note("keepalive: agent unreachable (%v)", rerr)
				}
			}
		}
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

// Forward opens a local TCP listener that relays every connection to target (a
// cluster host:port) through the tunnel. Used for raw-TCP services whose
// drivers can't be intercepted. Returns the bound local address.
func (t *Transport) Forward(ctx context.Context, listenAddr, target string, logf Logf) (string, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				remote, err := t.DialCluster(target)
				if err != nil {
					logf("forward %s: %v", target, err)
					c.Close()
					return
				}
				relay(c, remote)
			}()
		}
	}()
	return ln.Addr().String(), nil
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
func tofuHostKey(path, addr string, note func(string, ...any)) ssh.HostKeyCallback {
	if path == "" {
		return ssh.InsecureIgnoreHostKey()
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
				return fmt.Errorf("agent host key for %s changed (possible interception); "+
					"if you trust it, remove the %s line from %s", addr, addr, path)
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
