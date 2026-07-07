//go:build windows

package tun

import (
	"net"
	"os"
	"testing"
)

// TestPpidOfSelfWindows: the ToolHelp-based parent lookup must agree with the
// runtime for our own process.
func TestPpidOfSelfWindows(t *testing.T) {
	ppid, ok := ppidOf(os.Getpid())
	if !ok {
		t.Skip("ppidOf unavailable in this environment")
	}
	if ppid != os.Getppid() {
		t.Fatalf("ppidOf(self) = %d, want %d", ppid, os.Getppid())
	}
}

// TestPidForLocalPortSelfWindows proves the GetExtendedTcpTable lookup against a
// real socket: a local connection's source port must resolve back to THIS process.
func TestPidForLocalPortSelfWindows(t *testing.T) {
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

	pid, ok := pidForLocalPort(srcPort)
	if !ok {
		t.Skip("GetExtendedTcpTable returned nothing (unavailable in this environment)")
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForLocalPort(%d) = %d, want self %d", srcPort, pid, os.Getpid())
	}
}
