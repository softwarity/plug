//go:build darwin

package tun

import (
	"net"
	"net/netip"
	"os"
	"testing"
)

// TestPidForFlowSelf proves the macOS connect-time lookup against a real
// socket: a local connection must resolve back to THIS process.
func TestPidForFlowSelf(t *testing.T) {
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
		mustWorkInCI(t, ok, "the socket table lookup (netstat)")
		return
	}
	if pid != os.Getpid() {
		t.Fatalf("pidForFlow(%v) = %d, want self %d", f, pid, os.Getpid())
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
tcp4       0      0  *.61820                *.*                    LISTEN             0            0  131072  131072      node:90099  00001
tcp4       0      0  192.168.1.48.61820     192.0.2.9.5432         ESTABLISHED        0            0  131072  131072      psql:90100  00102
tcp4       0      0  192.168.1.48.61820     198.18.0.7.5432        SYN_SENT           0            0  131072  131072      psql:90101  00104
`

// to is the flow from a source port to the destination its row names.
func to(port uint16, dst string) flow {
	return flow{srcPort: port, dst: netip.MustParseAddrPort(dst)}
}

// TestPidFromNetstat pins the six shapes the column layout can take. Each one
// broke a plausible parser: the state at attribution time is SYN_SENT and not
// ESTABLISHED, IPv6 separates its port with a dot where the address is full of
// colons, a listener has no address at all, a process name can be three fields or
// look like a version number, and a row can have no owner. The last case is the
// one worth staring at: read the process column by its index and an ownerless row
// hands back the hex state column instead, where "00102" parses cleanly as pid 102.
func TestPidFromNetstat(t *testing.T) {
	for _, c := range []struct {
		why string
		f   flow
		pid int
		ok  bool
	}{
		{"SYN_SENT is the state a flow is in when plug asks", to(61761, "192.0.2.1:80"), 90091, true},
		{"an established IPv4 connection", to(61763, "127.0.0.1:61762"), 90092, true},
		{"IPv6 puts the port after a dot, behind a colon-filled address", to(61765, "[::1]:61764"), 90093, true},
		{"a listener is no flow: it has `*.*` where a destination would be", to(8080, "192.0.2.1:8080"), 0, false},
		{"a process named like a version number", to(61776, "160.79.104.10:443"), 90095, true},
		{"a process name holding spaces spans three fields", to(61780, "93.184.216.34:443"), 90096, true},
		{"no owner recorded: unattributable, not mis-attributed", to(61790, "93.184.216.34:443"), 0, false},
		{"a nameless process still owns its socket, or blanking a name evades the check", to(61800, "93.184.216.34:443"), 90098, true},
		{"pid 0 owns no socket", to(61810, "93.184.216.34:443"), 0, false},
		{"a port nobody holds", to(65000, "93.184.216.34:443"), 0, false},
		{"the right port to another destination is another socket", to(61761, "192.0.2.2:80"), 0, false},
	} {
		pid, ok := pidFromNetstat(netstatSample, c.f)
		if ok != c.ok || pid != c.pid {
			t.Errorf("pidFromNetstat(%v) = %d,%v; want %d,%v (%s)", c.f, pid, ok, c.pid, c.ok, c.why)
		}
	}
}

// One port, three sockets: a listener, a connection to somewhere else, and the
// flow. The first two are listed before it, and taking the port alone answered
// with the listener's owner, routing the flow by a process that never made it.
func TestPidFromNetstatTakesTheFlowsOwnSocket(t *testing.T) {
	pid, ok := pidFromNetstat(netstatSample, to(61820, "198.18.0.7:5432"))
	if !ok || pid != 90101 {
		t.Fatalf("pidFromNetstat(61820 → 198.18.0.7:5432) = %d,%v; want 90101,true: the listener and the other connection on 61820 are not this flow", pid, ok)
	}
}

// TestPidFromNetstatPortIsNotASuffix is separate because it is the failure that
// would never be noticed. Port 761 and port 61761 both end in "761": matching on
// the digits alone attributes the flow of one process to another, silently and
// only sometimes. The dot is what anchors it.
func TestPidFromNetstatPortIsNotASuffix(t *testing.T) {
	pid, ok := pidFromNetstat(netstatSample, to(761, "93.184.216.34:443"))
	if !ok || pid != 90097 {
		t.Fatalf("pidFromNetstat(761) = %d,%v; want 90097,true; 61761 is not port 761", pid, ok)
	}
}

// uidOf answers on the SYN of every new connection, so it must be right and it
// must be cheap. It asks the kernel now instead of forking ps; this proves the
// answer did not change with the method.
func TestUidOfAgreesWithTheSystem(t *testing.T) {
	if got, ok := uidOf(os.Getpid()); !ok || got != os.Getuid() {
		t.Fatalf("uidOf(self) = %d,%v; want %d,true", got, ok, os.Getuid())
	}
	// pid 1 is launchd, root, and always there: a second account to tell the
	// first from, without spawning anything.
	if got, ok := uidOf(1); !ok || got != 0 {
		t.Errorf("uidOf(1) = %d,%v; want 0,true (launchd runs as root)", got, ok)
	}
	// A pid that cannot exist must not answer. Silence here is what soleAllows
	// reads as "cannot establish", and it lets the flow through - the same as a
	// failed ps did, and the reason a wrong answer would be worse than none.
	if _, ok := uidOf(1 << 30); ok {
		t.Error("uidOf named an owner for a pid that does not exist")
	}
	// And a dead one, which is the case that actually happens: the process ends
	// between the SYN and the lookup.
	if _, ok := uidOf(spawnAndKill(t)); ok {
		t.Error("uidOf named an owner for a process that had exited")
	}
}
