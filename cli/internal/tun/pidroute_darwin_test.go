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
		mustWorkInCI(t, ok, "the socket table lookup (netstat)")
		return
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForLocalPort(%d) = %d, want self %d", srcPort, pid, os.Getpid())
	}
}

// netstatSample is `netstat -anv -p tcp` as macOS prints it, cut down to the rows
// that decide whether the parser is right. Written out rather than captured: a
// capture would carry whoever ran it around the network.
const netstatSample = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)      rxbytes      txbytes  rhiwat  shiwat   process:pid    state
tcp4       0      0  192.168.1.48.61761     192.0.2.1.80           SYN_SENT           0            0  131072  131072    Python:90091  00104
tcp4       0      0  127.0.0.1.61763        127.0.0.1.61762        ESTABLISHED        0            0  408300  146988    Python:90092  00002
tcp6       0      0  ::1.61765              ::1.61764              ESTABLISHED        0            0  407800  146808    Python:90093  00002
tcp4       0      0  *.8080                 *.*                    LISTEN             0            0  131072  131072      node:90094  00001
tcp4       0      0  192.168.1.48.61776     160.79.104.10.443      ESTABLISHED     2792         1853  131072  131768   2.1.251:90095  00102
tcp4       0      0  192.168.1.48.61780     93.184.216.34.443      ESTABLISHED      100          200  131072  131072 Google Chrome He:90096  00102
tcp4       0      0  192.168.1.48.61790     93.184.216.34.443      TIME_WAIT          0            0  131072  131072                  00102
tcp4       0      0  192.168.1.48.761       93.184.216.34.443      ESTABLISHED        0            0  131072  131072      curl:90097  00102
tcp4       0      0  192.168.1.48.61800     93.184.216.34.443      ESTABLISHED        0            0  131072  131072          :90098  00102
tcp4       0      0  192.168.1.48.61810     93.184.216.34.443      ESTABLISHED        0            0  131072  131072    kernel:0  00102
`

// TestPidFromNetstat pins the six shapes the column layout can take. Each one
// broke a plausible parser: the state at attribution time is SYN_SENT and not
// ESTABLISHED, IPv6 separates its port with a dot where the address is full of
// colons, a listener has no address at all, a process name can be three fields or
// look like a version number, and a row can have no owner. The last case is the
// one worth staring at: read the process column by its index and an ownerless row
// hands back the hex state column instead, where "00102" parses cleanly as pid 102.
func TestPidFromNetstat(t *testing.T) {
	for _, c := range []struct {
		why  string
		port uint16
		pid  int
		ok   bool
	}{
		{"SYN_SENT is the state a flow is in when plug asks", 61761, 90091, true},
		{"an established IPv4 connection", 61763, 90092, true},
		{"IPv6 puts the port after a dot, behind a colon-filled address", 61765, 90093, true},
		{"a listener has `*.port` where an address would be", 8080, 90094, true},
		{"a process named like a version number", 61776, 90095, true},
		{"a process name holding spaces spans three fields", 61780, 90096, true},
		{"no owner recorded: unattributable, not mis-attributed", 61790, 0, false},
		{"a nameless process still owns its socket, or blanking a name evades the check", 61800, 90098, true},
		{"pid 0 owns no socket", 61810, 0, false},
		{"a port nobody holds", 65000, 0, false},
	} {
		pid, ok := pidFromNetstat(netstatSample, c.port)
		if ok != c.ok || pid != c.pid {
			t.Errorf("pidFromNetstat(%d) = %d,%v; want %d,%v (%s)", c.port, pid, ok, c.pid, c.ok, c.why)
		}
	}
}

// TestPidFromNetstatPortIsNotASuffix is separate because it is the failure that
// would never be noticed. Port 761 and port 61761 both end in "761": matching on
// the digits alone attributes the flow of one process to another, silently and
// only sometimes. The dot is what anchors it.
func TestPidFromNetstatPortIsNotASuffix(t *testing.T) {
	pid, ok := pidFromNetstat(netstatSample, 761)
	if !ok || pid != 90097 {
		t.Fatalf("pidFromNetstat(761) = %d,%v; want 90097,true; 61761 is not port 761", pid, ok)
	}
}
