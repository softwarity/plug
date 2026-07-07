//go:build darwin

package tun

import (
	"net"
	"os"
	"testing"
)

// TestPidForLocalPortSelf proves the macOS connect-time lookup against a real
// socket: a local connection's source port must resolve back to THIS process.
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
		t.Skip("lsof did not find the socket (unavailable in this environment)")
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForLocalPort(%d) = %d, want self %d", srcPort, pid, os.Getpid())
	}
}
