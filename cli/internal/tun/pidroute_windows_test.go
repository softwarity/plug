//go:build windows

package tun

import (
	"net"
	"net/netip"
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

// TestProcStartSelfWindows: the GetProcessTimes-based creation stamp must be a
// positive, readable value for our own process (feeds the recycled-PID guard).
func TestProcStartSelfWindows(t *testing.T) {
	st, ok := procStart(os.Getpid())
	if !ok {
		t.Skip("procStart unavailable in this environment")
	}
	if st <= 0 {
		t.Fatalf("procStart(self) = %d, want > 0", st)
	}
}

// TestPidForFlowSelfWindows proves the GetExtendedTcpTable lookup against a
// real socket: a local connection must resolve back to THIS process.
func TestPidForFlowSelfWindows(t *testing.T) {
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
	f := flowOf(conn)

	pid, ok := pidForFlow(f)
	if !ok {
		t.Skip("GetExtendedTcpTable returned nothing (unavailable in this environment)")
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForFlow(%v) = %d, want self %d", f, pid, os.Getpid())
	}
}

// row is a MIB_TCPROW_OWNER_PID as Windows lays it in memory: ports in network
// order in the low 16 bits, the address's bytes in network order.
func row(localPort uint16, remote string, pid uint32) mibTCPRowOwnerPID {
	ap := netip.MustParseAddrPort(remote)
	a := ap.Addr().As4()
	htons := func(p uint16) uint32 { return uint32(p>>8) | uint32(p&0xff)<<8 }
	return mibTCPRowOwnerPID{
		localPort:  htons(localPort),
		remoteAddr: uint32(a[0]) | uint32(a[1])<<8 | uint32(a[2])<<16 | uint32(a[3])<<24,
		remotePort: htons(ap.Port()),
		owningPid:  pid,
	}
}

// One port, three sockets: a listener (remote 0.0.0.0:0), a connection to
// somewhere else, and the flow. The first two come first in the table, and
// taking the port alone answered with the listener's owner.
func TestPidFromTCPRowsTakesTheFlowsOwnSocket(t *testing.T) {
	rows := []mibTCPRowOwnerPID{
		row(61820, "0.0.0.0:0", 7001),
		row(61820, "192.0.2.9:5432", 7002),
		row(61820, "198.18.0.7:5432", 7003),
	}
	f := flow{srcPort: 61820, dst: netip.MustParseAddrPort("198.18.0.7:5432")}
	if pid, ok := pidFromTCPRows(rows, f); !ok || pid != 7003 {
		t.Fatalf("pidFromTCPRows = %d,%v; want 7003,true: the listener and the other connection on 61820 are not this flow", pid, ok)
	}
	f.dst = netip.MustParseAddrPort("198.18.0.8:5432")
	if pid, ok := pidFromTCPRows(rows, f); ok {
		t.Fatalf("a flow no row names was attributed to %d", pid)
	}
}
