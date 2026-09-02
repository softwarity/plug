//go:build darwin

package tun

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// procStart returns pid's start time as Unix seconds, via `ps -o lstart=` (cgo-free,
// like ppidOf). Only comparability feeds the ancestry walk, not the unit, so a
// coarse second granularity is enough to catch a recycled PID — and two processes
// born in the same second compare equal (<=), never a false "younger parent".
// LANG/LC pinned to C so we parse the fixed English date form regardless of locale.
func procStart(pid int) (int64, bool) {
	cmd := exec.Command(HelperPath("ps"), "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	t, err := time.Parse("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// ppidOf returns pid's parent via `ps -o ppid=`. Cgo-free on purpose — the
// datapath builds with CGO_ENABLED=0, so we avoid libproc. The parent walk is a
// few hops and its result is cached per connection, so the exec cost amortizes.
func ppidOf(pid int) (int, bool) {
	out, err := exec.Command(HelperPath("ps"), "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// pidForLocalPort maps a local TCP source port to the PID owning that socket, via
// `netstat -anv` (cgo-free; the datapath builds with CGO_ENABLED=0). `-a` includes
// listeners, `-n` keeps it numeric, `-v` is what adds the `process:pid` column.
// The daemon runs as root, so it sees every process's sockets, though this one
// does not need the privilege: the owning pid is recorded in the kernel's PCB
// list, which netstat reads and any user may read.
//
// This used to shell out to lsof, and netstat replaces it on both counts.
//
// COST. lsof answers by walking every process's file descriptors, so it pays for
// the whole machine to answer about one port: measured at 92ms on a 289-socket
// desktop, on the SYN of every new connection, which capped plug at about a dozen
// connections a second. netstat reads the PCB list the kernel already keeps and
// answers the same question in 4ms. Windows was never in this position, since it
// asks GetExtendedTcpTable, one syscall, and this brings macOS closer to that
// shape.
//
// CORRECTNESS, which matters more. lsof lists open DESCRIPTORS; netstat lists live
// CONNECTIONS. A process that has not closed the file descriptor of a finished
// connection still shows up in lsof, in state CLOSED, holding a port the kernel
// has already released and may already have handed to somebody else. lsof reported
// three such ghosts on the machine this was written on. Attributing a flow through
// one of them means answering with the pid of whoever used that port BEFORE the
// process actually making the connection: the wrong cluster in the multicluster
// router, and the wrong owner in the single-cluster check. Reading the kernel's
// table cannot produce that answer, because a port with no live PCB has no row.
func pidForLocalPort(srcPort uint16) (int, bool) {
	out, err := exec.Command(HelperPath("netstat"), "-anv", "-p", "tcp").Output()
	if err != nil {
		return 0, false
	}
	return pidFromNetstat(string(out), srcPort)
}

// pidFromNetstat is the parsing half, separated so it can be tested against
// captured output instead of whatever this machine happens to be doing.
//
// A row is: proto, Recv-Q, Send-Q, local, foreign, state, four counters, then
// `process:pid`, then hex columns. Only the local address and the pid are read,
// and neither is taken by column number:
//
//   - The port is what follows the LAST dot of the local address, which holds for
//     `192.168.1.48.61761`, for `::1.61765` (IPv6 separates its port with a dot
//     too, not a colon) and for a listener's `*.8080`.
//   - The pid is what follows the last colon of the rightmost field shaped like
//     `name:digits`, searched from the right and never before the state column.
//     A process name may contain spaces ("Google Chrome He:1234" is three fields)
//     or look like nothing at all (lsof and netstat both name one process here
//     "2.1.251"), and rows exist with no process column whatsoever. Scanning from
//     the right lands on the pid in all three cases and finds nothing in the last,
//     which is the honest answer: no owner, so no attribution.
func pidFromNetstat(out string, srcPort uint16) (int, bool) {
	want := "." + strconv.Itoa(int(srcPort))
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// Anything shorter is a header or a truncated line; the two address
		// columns must exist before the scan below can start after them.
		if len(f) < 6 || !strings.HasPrefix(f[0], "tcp") {
			continue
		}
		if !strings.HasSuffix(f[3], want) {
			continue
		}
		// From the right, so the process field is reached before anything else,
		// and stopping at the state column so the two addresses are never read as
		// an owner. No IPv6 address can pass the check below anyway, since what
		// follows its last colon always holds the dot before the port; the bound
		// is there so that stays true of a format nobody has seen yet.
		for i := len(f) - 1; i >= 5; i-- {
			c := strings.LastIndexByte(f[i], ':')
			// c == 0 is a process with no name at all, which is still an owner:
			// refusing to read it would report the socket as unattributed, and
			// unattributed is what soleAllows lets through. A process that can
			// blank its own name must not become a process that outranks the
			// check. Only a pid of 0 is rejected, which no socket has.
			if c < 0 || c == len(f[i])-1 {
				continue
			}
			if pid, err := strconv.Atoi(f[i][c+1:]); err == nil && pid > 0 {
				return pid, true
			}
		}
		return 0, false // the row is ours, but nothing owns it
	}
	return 0, false
}

// uidOf returns the account a process runs under, via `ps -o uid=`. Same
// cgo-free shape as ppidOf, and asked only on the single-cluster shortcut, where
// the ancestry walk does not run at all.
func uidOf(pid int) (int, bool) {
	out, err := exec.Command(HelperPath("ps"), "-o", "uid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return uid, true
}
