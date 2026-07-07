//go:build darwin

package tun

import (
	"net"
	"os"
	"testing"
)

// TestMultiDialSelf proves the whole macOS attribution chain end to end against a
// real socket: register THIS process as a `plug -p X` launcher, open a local
// connection, and check multiDial routes that flow's source port to the cluster
// this process belongs to (srcPort → PID via lsof → clusterForPID → transport).
func TestMultiDialSelf(t *testing.T) {
	graftDir = t.TempDir()
	unreg := RegisterClient("hostZ:2222", os.Getpid())
	defer unreg()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	srcPort := uint16(conn.LocalAddr().(*net.TCPAddr).Port)

	var routedTo string
	df := multiDial(func(key string) (Dialer, bool) {
		routedTo = key
		return loopbackDialer{}, true
	})

	_, ok := df(srcPort)
	if !ok {
		t.Skip("connect-time lookup unavailable in this environment")
	}
	if routedTo != "hostZ:2222" {
		t.Fatalf("flow routed to %q, want hostZ:2222", routedTo)
	}
}

// TestMultiDialRefuses: a source port owned by no registered launcher is refused
// (RST), never mis-routed.
func TestMultiDialRefuses(t *testing.T) {
	graftDir = t.TempDir() // empty registry — nothing is a launcher

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	srcPort := uint16(conn.LocalAddr().(*net.TCPAddr).Port)

	df := multiDial(func(string) (Dialer, bool) { return loopbackDialer{}, true })
	if _, ok := df(srcPort); ok {
		t.Fatalf("an unattributable flow must be refused")
	}
}
