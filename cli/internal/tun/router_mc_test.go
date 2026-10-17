//go:build darwin

package tun

import (
	"net"
	"os"
	"testing"
)

// localSocket opens a real local TCP connection and returns its flow + a
// cleanup. The socket table lookup must see it and map it back to this process.
func localSocket(t *testing.T) (flow, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	return flowOf(conn), func() { conn.Close(); ln.Close() }
}

// TestMultiDialSelf proves the whole macOS attribution chain against a real
// socket, with TWO clusters live (so sole() doesn't short-circuit): register THIS
// process as a launcher of hostZ, and check the flow routes to hostZ's transport.
func TestMultiDialSelf(t *testing.T) {
	graftDir = t.TempDir()
	unreg := RegisterClient("hostZ:2222", os.Getpid(), "")
	defer unreg()

	ct := NewClusterTransports()
	ct.Set("hostZ:2222", loopbackDialer{addr: "Z"})
	ct.Set("hostX:2222", loopbackDialer{addr: "X"}) // 2 clusters → attribution

	f, done := localSocket(t)
	defer done()

	d, key, ok := multiDial(ct)(f, "svc")
	if !ok {
		mustWorkInCI(t, ok, "the connect-time attribution")
		return
	}
	if key != "hostZ:2222" {
		t.Fatalf("attributed to %q, want hostZ:2222", key)
	}
	if lb, _ := d.(loopbackDialer); lb.addr != "Z" {
		t.Fatalf("flow routed to %q, want hostZ (Z)", lb.addr)
	}
}

// TestMultiDialRefuses: with ≥2 clusters, a source port owned by no registered
// launcher is refused (RST), never mis-routed.
func TestMultiDialRefuses(t *testing.T) {
	graftDir = t.TempDir() // empty registry — nothing is a launcher
	ct := NewClusterTransports()
	ct.Set("hostA:2222", loopbackDialer{addr: "A"})
	ct.Set("hostB:2222", loopbackDialer{addr: "B"}) // ≥2 → attribution required

	f, done := localSocket(t)
	defer done()

	if _, _, ok := multiDial(ct)(f, "svc"); ok {
		t.Fatalf("an unattributable flow must be refused when >1 cluster is active")
	}
}

// TestMultiDialSoleTransparent: a SINGLE active cluster routes transparently with
// no attribution — the mono-cluster non-regression (a detached child still works).
func TestMultiDialSoleTransparent(t *testing.T) {
	graftDir = t.TempDir() // no launcher registered at all
	ct := NewClusterTransports()
	ct.Set("only:2222", loopbackDialer{addr: "only"})

	d, key, ok := multiDial(ct)(at(40000), "svc") // arbitrary port, no attribution needed
	if !ok {
		t.Fatal("a single active cluster must route transparently")
	}
	if key != "only:2222" {
		t.Fatalf("sole cluster key = %q, want only:2222", key)
	}
	if lb, _ := d.(loopbackDialer); lb.addr != "only" {
		t.Fatalf("routed to %q, want only", lb.addr)
	}
}

// The only tunnel is not the only cluster. This process is a client of a
// cluster that has no tunnel (its agent refused the key), while another
// cluster's tunnel lingers through its grace: the flow must be refused, never
// sent out through the other cluster's tunnel.
func TestMultiDialNeverLendsAnotherClustersTunnel(t *testing.T) {
	graftDir = t.TempDir()
	unreg := RegisterClient("refused:2230", os.Getpid(), "")
	defer unreg()
	ct := NewClusterTransports()
	ct.Set("other:2227", loopbackDialer{addr: "other"})
	ct.SetActive([]string{"refused:2230", "other:2227"})

	f, done := localSocket(t)
	defer done()
	if d, key, ok := multiDial(ct)(f, "svc"); ok {
		t.Fatalf("a flow of a cluster with no tunnel went out through %q (%v)", key, d)
	}

	// And the cluster still in use alone keeps the transparent route.
	ct.SetActive([]string{"other:2227"})
	if _, key, ok := multiDial(ct)(at(40000), "svc"); !ok || key != "other:2227" {
		t.Fatalf("the one cluster in use lost its transparent route: %q %v", key, ok)
	}
}
