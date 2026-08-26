package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// rejectingServer is a minimal in-process SSH server that ACCEPTS connections
// but REJECTS every direct-tcpip channel — the agent-side shape of "the cluster
// name doesn't resolve / the port is refused". It counts connections so the
// test can assert the client did NOT reconnect on a channel rejection.
func rejectingServer(t *testing.T) (addr string, conns *atomic.Int32) {
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
	t.Cleanup(func() { ln.Close() })

	conns = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					_ = nc.Reject(ssh.Prohibited, "open failed") // every channel bounces
				}
			}(c)
		}
	}()
	return ln.Addr().String(), conns
}

// clientKeyPEM generates a throwaway ed25519 client key in OpenSSH PEM form —
// what Dial expects as its embedded key.
func clientKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// A channel REJECTED by a healthy agent must surface as *ssh.OpenChannelError
// and must NOT tear down / re-dial the shared connection: reconnecting on it
// killed every concurrent channel whenever Windows' WPAD/mDNS probes hit a
// name the agent refuses (the 1.x cascade). The rejecting server counts
// connections: one dial, N rejections, still one connection.
func TestDialContextChannelRejectDoesNotReconnect(t *testing.T) {
	addr, conns := rejectingServer(t)
	host, port, _ := net.SplitHostPort(addr)

	tr, err := Dial(host, port, "plug", [][]byte{clientKeyPEM(t)}, "", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tr.Close()
	if got := conns.Load(); got != 1 {
		t.Fatalf("after Dial: want 1 server connection, got %d", got)
	}

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = tr.DialContext(ctx, "tcp", "ghost:80")
		cancel()
		if err == nil {
			t.Fatal("a rejected channel must error")
		}
		var oce *ssh.OpenChannelError
		if !errors.As(err, &oce) {
			t.Fatalf("want *ssh.OpenChannelError, got %T: %v", err, err)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("channel rejections must not reconnect: want 1 connection, got %d", got)
	}
}
