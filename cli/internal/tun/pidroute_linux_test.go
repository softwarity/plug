//go:build linux

package tun

import (
	"net"
	"os"
	"testing"
)

// The Linux connect-time lookup, against a real socket: a local connection's
// source port must resolve back to THIS process.
//
// This is the reference implementation of the primitive the multicluster router
// uses, and it is the one platform where the router itself is not built yet, so
// the function has no caller and had no test. The coverage matrix on the site
// nonetheless listed it as working and unit-tested on all three platforms, which
// was true of two of them. Rather than delete an implementation somebody will
// need the day the Linux daemon lands, the claim is made true.
//
// Two functions are exercised through it: the /proc/net/tcp scan that turns a
// port into a socket inode, and the walk over /proc/<pid>/fd that turns that
// inode into a pid.
func TestPidForLocalPortSelf(t *testing.T) {
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
	srcPort := conn.LocalAddr().(*net.TCPAddr).Port

	pid, ok := pidForLocalPort(uint16(srcPort))
	if !ok {
		mustWorkInCI(t, ok, "the /proc socket table lookup")
		return
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForLocalPort(%d) = %d, want self %d. A wrong answer here does not fail a lookup, "+
			"it attributes somebody's traffic to the wrong cluster", srcPort, pid, os.Getpid())
	}
}

// A port nothing is listening on must answer "no", not a stale or invented pid:
// the router refuses a flow it cannot attribute, and it can only do that if the
// primitive says so rather than guessing.
func TestPidForLocalPortRefusesAnUnusedPort(t *testing.T) {
	// Bind and release, so the port is real, plausible, and no longer in use.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if pid, ok := pidForLocalPort(uint16(port)); ok {
		t.Errorf("a port nobody holds resolved to pid %d; the router would route that flow somewhere", pid)
	}
}

// The inode lookup is the half that reads /proc/net/tcp, and it must find the
// socket by its own port rather than by position or by luck.
func TestInodeForLocalPortFindsTheRightSocket(t *testing.T) {
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

	inode, ok := inodeForLocalPort(srcPort)
	if !ok {
		mustWorkInCI(t, ok, "the /proc/net/tcp scan")
		return
	}
	if inode == "" {
		t.Fatal("the scan reported success with no inode")
	}
	// And the same inode has to lead back to this process, or the two halves
	// agree on nothing.
	if pid, ok := pidForSocketInode(inode); !ok || pid != os.Getpid() {
		t.Errorf("inode %s led to pid %d (ok=%v), want self %d", inode, pid, ok, os.Getpid())
	}
}
