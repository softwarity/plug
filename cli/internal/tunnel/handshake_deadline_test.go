package tunnel

import (
	"net"
	"sync"
	"testing"
	"time"
)

// blackHoleServer accepts TCP connections and then says NOTHING, ever. It is the
// agent that is still starting, the load balancer holding a socket open to a
// dead backend, the filter that lets SYN through and drops the rest.
//
// It keeps every connection open on purpose: closing them would make the client
// fail for a reason that is not the one under test.
func blackHoleServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().String()
}

// An agent that answers the TCP handshake and then goes quiet must not hold plug
// forever.
//
// x/crypto/ssh gives cfg.Timeout to net.DialTimeout and stops there: the banner
// exchange, the key exchange and the authentication that follow have no deadline
// of any kind. So the connection is established, the handshake never completes,
// and plug waits with no timeout, alive and silent, having never run the command
// it was given. A soak run caught exactly that shape once - twelve minutes, zero
// rounds - which is what sent us looking here.
//
// The test asserts the ONLY thing that matters: dial returns. Without the
// deadline in dial() it does not, and this test hangs until the go test timeout.
func TestASilentAgentDoesNotHoldTheDialForever(t *testing.T) {
	addr := blackHoleServer(t)
	host, port, _ := net.SplitHostPort(addr)

	restore := dialTimeout
	dialTimeout = 300 * time.Millisecond
	defer func() { dialTimeout = restore }()

	tr := &Transport{host: host, port: port, user: "plug", keys: [][]byte{clientKeyPEM(t)}}
	done := make(chan error, 1)
	go func() { _, err := tr.dial(); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dial succeeded against a server that never spoke")
		}
	case <-time.After(10 * time.Second):
		// Generous on purpose: 300ms of deadline against ten seconds of patience
		// means a failure here is the deadline missing, never a slow machine.
		t.Fatal("dial never returned: the SSH handshake is unbounded, so a silent agent hangs plug forever")
	}
}

// The deadline must be CLEARED once the handshake succeeds, or the session it
// was protecting dies fifteen seconds later - the mistake this shape invites,
// and a worse bug than the one being fixed.
//
// Asserting that is not as direct as it looks, and the first version of this
// test proved nothing: the transport is SELF-HEALING, so a connection dying of a
// forgotten deadline is silently replaced and every call still succeeds. What
// the bug actually produces is a session that redials for ever, once per
// deadline, so the thing to count is CONNECTIONS. The rejecting server counts
// them: one dial, and one still after the deadline has come and gone.
func TestTheHandshakeDeadlineDoesNotOutliveTheHandshake(t *testing.T) {
	addr, conns := rejectingServer(t)
	host, port, _ := net.SplitHostPort(addr)

	restore := dialTimeout
	dialTimeout = 300 * time.Millisecond
	defer func() { dialTimeout = restore }()

	tr, err := Dial(host, port, "plug", [][]byte{clientKeyPEM(t)}, "", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tr.Close()

	// Three deadlines' worth of doing nothing, then use the session.
	time.Sleep(900 * time.Millisecond)
	_, err = tr.DialContext(t.Context(), "tcp", "whatever:80")
	if err == nil {
		t.Fatal("the rejecting server accepted a channel")
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("the server saw %d connections, want 1: the handshake deadline outlived the handshake, "+
			"so the session dies and redials every %v", n, dialTimeout)
	}
}
