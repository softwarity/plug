// Package tunnel implements the plug data path: an SSH transport to the agent
// (this file) plus, later, a userspace TUN + netstack that turns captured
// cluster-bound flows into per-flow SSH direct-tcpip channels.
//
// The transport carries everything over a single SSH connection to the agent's
// sshd. Each outbound flow (and every DNS query, over TCP) becomes a
// direct-tcpip channel, so sshd itself opens the real connection from inside
// the cluster — no server code of ours, and no sshuttle/Python.
package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// clusterResolver is the Docker/Swarm embedded DNS, reachable in TCP from
// inside the agent. DNS queries captured on the TUN are forwarded here.
const clusterResolver = "127.0.0.11:53"

// Logf is where the data paths report progress; set by the caller.
type Logf func(format string, a ...any)

// relay copies bidirectionally and closes both ends when either side finishes.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
}

// Transport is an SSH connection to the agent used as a demux for outbound
// cluster traffic. It is safe for concurrent use: ssh.Client multiplexes
// channels over the one connection.
type Transport struct {
	client *ssh.Client
}

// Dial opens the SSH transport to the agent as the tunnel user, authenticating
// with the embedded private key. Host keys are not verified, consistent with
// plug's no-secret model (see the Security page).
func Dial(host, port, user string, privateKey []byte) (*Transport, error) {
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parsing embedded key: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, port), cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh to %s:%s: %w", host, port, err)
	}
	return &Transport{client: client}, nil
}

// DialContext opens a connection to addr from inside the cluster via a
// direct-tcpip channel. network must be "tcp". This is the single primitive
// the netstack layer calls for every captured TCP flow.
func (t *Transport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("tunnel: unsupported network %q", network)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := t.client.Dial("tcp", addr)
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

// DialCluster dials a cluster service:port through the tunnel.
func (t *Transport) DialCluster(addr string) (net.Conn, error) {
	return t.DialContext(context.Background(), "tcp", addr)
}

// Resolver returns a *net.Resolver that performs lookups against the cluster's
// embedded DNS over the tunnel (DNS over TCP). Use it to resolve cluster
// service names exactly as a container would.
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

// Forward opens a local TCP listener that relays every connection to target
// (a cluster host:port) through the tunnel. Used for raw-TCP services whose
// drivers ignore the SOCKS proxy (e.g. AMQP). Returns the bound local address.
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

// Close tears down the SSH connection (and every channel on it).
func (t *Transport) Close() error {
	if t.client == nil {
		return nil
	}
	return t.client.Close()
}
