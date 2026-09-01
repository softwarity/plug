package tunnel

import (
	"bytes"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// localService stands in for the process a -s mapping points at: a loopback
// listener whose port an Exposed can be aimed at, plus a count of how many
// flows ever reached it (the nonce must reach it zero times).
func localService(t *testing.T, serve func(net.Conn)) (port string, reached *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local service listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	if _, port, err = net.SplitHostPort(ln.Addr().String()); err != nil {
		t.Fatalf("local service address: %v", err)
	}
	reached = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			go func() {
				defer c.Close()
				serve(c)
			}()
		}
	}()
	return port, reached
}

// tcpPair is one loopback connection seen from both ends: `client` stands in
// for the cluster workload dialling the exposed name, `server` for what the
// agent's forward hands to handle. Real TCP, not net.Pipe, because half-close
// is exactly what these tests are about and net.Pipe has none.
func tcpPair(t *testing.T) (client, server *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pair listener: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("pair dial: %v", err)
	}
	s := <-accepted
	if s == nil {
		t.Fatal("pair accept failed")
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	return c.(*net.TCPConn), s.(*net.TCPConn)
}

// deadPort is a port nothing listens on: what a -s mapping points at while the
// user's process has not bound it yet.
func deadPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("borrowing a port: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("borrowed address: %v", err)
	}
	ln.Close()
	return port
}

// armed builds an Exposed pointed at port, with nonce armed when non-nil.
func armed(t *testing.T, port string, nonce []byte) *Exposed {
	t.Helper()
	e := &Exposed{
		spec: ExposeSpec{Name: "mail", ClusterPort: "80", LocalPort: port},
		hit:  make(chan struct{}, 1),
	}
	if nonce != nil {
		e.nonce.Store(&nonce)
	}
	return e
}

// closed reports whether the peer hung up, telling that apart from a flow left
// hanging (which shows up as the read deadline expiring, not as an error).
func closed(t *testing.T, c *net.TCPConn) bool {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	_, err := c.Read(make([]byte, 1))
	return err != nil && !os.IsTimeout(err)
}

// The self-test byte is protocol between plug and plug. Delivering it to the
// user's service would make Verify indistinguishable from a client speaking
// garbage: the service logs a parse error, or worse answers it.
func TestHandleSwallowsTheArmedNonce(t *testing.T) {
	port, reached := localService(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	nonce := []byte("0123456789abcdef")
	e := armed(t, port, nonce)

	client, server := tcpPair(t)
	go e.handle(server)
	if _, err := client.Write(nonce); err != nil {
		t.Fatalf("writing the nonce: %v", err)
	}

	select {
	case <-e.hit:
	case <-time.After(3 * time.Second):
		t.Fatal("the armed nonce raised no hit: Verify would call a working path broken")
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the nonce was relayed to the local service %d time(s)", n)
	}
	// Verify closes its end too, but only after the hit: a probe left spliced
	// would hold a channel open on the shared SSH connection for every retry.
	if !closed(t, client) {
		t.Fatal("the probe flow was left open after the hit; handle must close it")
	}
}

// A flow that says nothing within the sniff grace is a client waiting for the
// server to speak first (SMTP, SSH, most databases). It must be spliced anyway,
// and the bytes it sends afterwards must arrive whole: the sniff already
// consumed the first sixteen of them.
func TestHandleSplicesASilentFlowWithoutLosingBytes(t *testing.T) {
	const greeting = "220 local service ready\r\n"
	payload := []byte("HELO the-rest-past-the-sniffed-prefix\r\n") // longer than the nonce
	got := make(chan []byte, 1)
	port, _ := localService(t, func(c net.Conn) {
		c.Write([]byte(greeting))
		b := make([]byte, len(payload))
		n, _ := io.ReadFull(c, b)
		got <- b[:n]
	})
	nonce := []byte("0123456789abcdef")
	e := armed(t, port, nonce)

	client, server := tcpPair(t)
	go e.handle(server)

	// Say nothing. The greeting can only arrive once the sniff has given up and
	// dialled the local service, so reading it is what proves the grace expired,
	// with no sleep to make flaky.
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	hello := make([]byte, len(greeting))
	if _, err := io.ReadFull(client, hello); err != nil {
		t.Fatalf("no local greeting reached the silent client: %v", err)
	}
	if string(hello) != greeting {
		t.Fatalf("greeting mangled: %q", hello)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	select {
	case b := <-got:
		if !bytes.Equal(b, payload) {
			t.Fatalf("the local service got %q, want %q: the sniffed prefix was dropped, duplicated or padded", b, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the local service never received the request: the sniffed prefix was not re-attached")
	}
}

// The sniff reads a fixed sixteen bytes, so a flow that goes quiet after four
// leaves twelve bytes of untouched buffer behind. Handing the whole buffer to
// the local service pads the request with NULs, which is a corrupted request,
// not a short one.
func TestHandleReattachesOnlyTheBytesActuallySniffed(t *testing.T) {
	got := make(chan []byte, 1)
	port, _ := localService(t, func(c net.Conn) {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b, _ := io.ReadAll(c)
		got <- b
	})
	nonce := []byte("0123456789abcdef")
	e := armed(t, port, nonce)

	client, server := tcpPair(t)
	go e.handle(server)
	// Outlast the grace so the splice happens with the sniff still in flight,
	// then send fewer bytes than the nonce is long and stop talking.
	time.Sleep(700 * time.Millisecond)
	if _, err := client.Write([]byte("PING")); err != nil {
		t.Fatalf("writing the short request: %v", err)
	}
	client.CloseWrite()

	select {
	case b := <-got:
		if string(b) != "PING" {
			t.Fatalf("the local service got %q, want %q", b, "PING")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the short request never reached the local service")
	}
}

// Nothing listening locally must look like a container whose process has not
// bound its port: the connection is refused. Holding it open instead leaves the
// cluster client waiting on a service that does not exist.
func TestRelayLocalRefusesWhenTheLocalServiceIsAbsent(t *testing.T) {
	port := deadPort(t)
	for _, tc := range []struct {
		name  string
		nonce []byte
	}{
		{"steady state", nil},
		{"during the verify window", []byte("0123456789abcdef")}, // a sniff is in flight
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := armed(t, port, tc.nonce)
			client, server := tcpPair(t)
			done := make(chan struct{})
			go func() { e.handle(server); close(done) }()
			if !closed(t, client) {
				t.Fatal("the flow was left open with no local service to talk to")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handle never returned: a refused local dial must not park the goroutine")
			}
		})
	}
}

// A request-then-EOF protocol (HTTP/1.0, and every "read until the client shuts
// up" service) needs the shutdown to cross the splice. Without it the local
// service waits for an end-of-stream nobody will send, and the client waits for
// an answer that is never computed: a deadlock with both ends healthy.
func TestRelayLocalHalfClosesTowardTheLocalService(t *testing.T) {
	port, _ := localService(t, func(c net.Conn) {
		b, err := io.ReadAll(c) // returns only once the write half is shut
		if err != nil {
			return
		}
		c.Write(append([]byte("answer:"), b...))
	})
	e := armed(t, port, nil)

	client, server := tcpPair(t)
	go e.handle(server)
	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	client.CloseWrite()

	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("no answer came back: %v (the client's shutdown never reached the local service)", err)
	}
	if string(got) != "answer:request" {
		t.Fatalf("got %q, want %q", got, "answer:request")
	}
}

// The mirror case: the local service is done and the client still holds its
// write half open (a browser keeping the connection for a second request). The
// end of the response has to reach it as an end-of-stream, or it hangs waiting
// for a body that has already been fully sent.
func TestRelayLocalHalfClosesTowardTheClient(t *testing.T) {
	port, _ := localService(t, func(c net.Conn) { c.Write([]byte("banner")) })
	e := armed(t, port, nil)

	client, server := tcpPair(t)
	go e.handle(server)

	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(client) // never shuts its own write half
	if err != nil {
		t.Fatalf("the local service's end-of-stream never reached the client: %v", err)
	}
	if string(got) != "banner" {
		t.Fatalf("got %q, want %q", got, "banner")
	}
}
