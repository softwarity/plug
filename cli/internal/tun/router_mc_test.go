//go:build darwin

package tun

import (
	"net"
	"os"
	"testing"
)

// localSocket opens a real local TCP connection and returns its source port +
// a cleanup. The socket table lookup must see it and map it back to this process.
func localSocket(t *testing.T) (uint16, func()) {
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
	port := uint16(conn.LocalAddr().(*net.TCPAddr).Port)
	return port, func() { conn.Close(); ln.Close() }
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

	srcPort, done := localSocket(t)
	defer done()

	d, key, ok := multiDial(ct)(srcPort, "svc")
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

	srcPort, done := localSocket(t)
	defer done()

	if _, _, ok := multiDial(ct)(srcPort, "svc"); ok {
		t.Fatalf("an unattributable flow must be refused when >1 cluster is active")
	}
}

// TestMultiDialSoleTransparent: a SINGLE active cluster routes transparently with
// no attribution — the mono-cluster non-regression (a detached child still works).
func TestMultiDialSoleTransparent(t *testing.T) {
	graftDir = t.TempDir() // no launcher registered at all
	ct := NewClusterTransports()
	ct.Set("only:2222", loopbackDialer{addr: "only"})

	d, key, ok := multiDial(ct)(40000, "svc") // arbitrary port, no attribution needed
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

// A name pinned by a live client goes to that client's cluster before any
// attribution - the live-mount helper's, dialled by the kernel's SMB client on
// Windows, where no ancestry leads to a launcher. Unpinned, the flow takes the
// ordinary route; a dead pinner's pin is not read.
func TestPinnedNameRoutesToItsCluster(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	ct := NewClusterTransports()
	a, b := loopbackDialer{addr: "A"}, loopbackDialer{addr: "B"}
	ct.Set("cluster-a", a)
	ct.Set("cluster-b", b)
	unreg := RegisterClient("cluster-b", os.Getpid(), "")
	defer unreg()
	PinName("cluster-b", os.Getpid(), "Plug-Mnt-Web-1234abcd")
	defer UnpinNames("cluster-b", os.Getpid())
	pinCache = map[string]pinEntry{}
	d, key, ok := multiDial(ct)(40000, "plug-mnt-web-1234abcd")
	if !ok || key != "cluster-b" || d.(loopbackDialer).addr != "B" {
		t.Fatalf("pinned name: %v %q %v", ok, key, d)
	}
	// Not pinned: two clusters, an unattributable port → refused as before.
	pinCache = map[string]pinEntry{}
	if _, _, ok := multiDial(ct)(40000, "other"); ok {
		t.Fatal("an unpinned name with two clusters must still be attributed")
	}
	// A dead pinner: its pin is nobody's.
	UnpinNames("cluster-b", os.Getpid())
	PinName("cluster-b", 999999, "plug-mnt-web-1234abcd")
	pinCache = map[string]pinEntry{}
	if k, ok := pinnedCluster("plug-mnt-web-1234abcd"); ok {
		t.Fatalf("a dead client's pin was honoured: %q", k)
	}
}
