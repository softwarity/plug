//go:build darwin

package tun

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// procStart returns pid's start time in the unit `ps -o lstart=` gave it, read
// from the kernel (kern.proc.pid) instead of a fork of ps.
//
// THE UNIT IS ps's, ON PURPOSE. ps prints the start in LOCAL time, and plug used
// to parse that as if it were UTC, so its stamp is the true epoch plus the
// machine's offset at that moment (7200s here in summer). That never mattered for
// the ancestry walk, which only compares stamps. But the value is also WRITTEN
// DOWN, in the .start sidecar beside a client marker, and read back by whatever
// build runs later: a different unit would make every marker written by an older
// plug look like another process to this one, the cluster would read as unheld,
// and the one-cluster-one-account rule would stop applying to sessions already
// open across the upgrade. So the kernel's epoch gets the offset ps would have
// added, taken at the start instant as localtime(3) does, which gives the same
// number to the second (TestProcStartIsWhatPsSaid).
//
// What the fork cost: about 16ms, paid on EVERY multicluster flow (the stamp is
// the one thing the walk re-reads each time, see pidRouter.route) and on every
// registry check the stamp cache does not cover. Two processes born in the same
// second still compare equal (<=), never a false "younger parent".
func procStart(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	sec := kp.Proc.P_starttime.Sec
	_, offset := time.Unix(sec, 0).Zone()
	return sec + int64(offset), true
}

// ppidOf returns pid's parent from the kernel (kern.proc.pid), cgo-free like
// uidOf: the datapath builds with CGO_ENABLED=0, which rules out libproc but
// not sysctl. It used to fork `ps -o ppid=`, a fork per hop of the walk.
func ppidOf(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}

// pidForFlow maps a flow to the PID owning its socket, via `netstat -anv`
// (cgo-free; the datapath builds with CGO_ENABLED=0). `-a` includes listeners,
// `-n` keeps it numeric, `-v` is what adds the `process:pid` column.
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
func pidForFlow(f flow) (int, bool) {
	out, err := exec.Command(HelperPath("netstat"), "-anv", "-p", "tcp").Output()
	if err != nil {
		return 0, false
	}
	return pidFromNetstat(string(out), f)
}

// pidFromNetstat is the parsing half, separated so it can be tested against
// captured output instead of whatever this machine happens to be doing.
//
// A row is: proto, Recv-Q, Send-Q, local, foreign, state, four counters, then
// `process:pid`, then hex columns. The two addresses and the pid are read:
//
//   - The row is the flow's when its local address ends in `.<source port>` and
//     its foreign address is exactly the flow's destination, `<fake ip>.<port>`.
//     The port alone named a listener's `*.8080` just as well, or another
//     socket of the same port to somewhere else, and the first such row listed
//     answered for the flow: the wrong cluster in the multicluster router, the
//     wrong owner in the single-cluster check. The fake destination is minted
//     by plug, so no other socket has it.
//   - The port is what follows the LAST dot of the local address, which holds for
//     `192.168.1.48.61761` and for `::1.61765` (IPv6 separates its port with a
//     dot too, not a colon).
//   - The pid is what follows the last colon of the rightmost field shaped like
//     `name:digits`, searched from the right and never before the state column.
//     A process name may contain spaces ("Google Chrome He:1234" is three fields)
//     or look like nothing at all (lsof and netstat both name one process here
//     "2.1.251"), and rows exist with no process column whatsoever. Scanning from
//     the right lands on the pid in all three cases and finds nothing in the last,
//     which is the honest answer: no owner, so no attribution.
func pidFromNetstat(out string, fl flow) (int, bool) {
	want := "." + strconv.Itoa(int(fl.srcPort))
	dst := fl.dst.Addr().String() + "." + strconv.Itoa(int(fl.dst.Port()))
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// Anything shorter is a header or a truncated line; the two address
		// columns must exist before the scan below can start after them.
		if len(f) < 6 || !strings.HasPrefix(f[0], "tcp") {
			continue
		}
		if !strings.HasSuffix(f[3], want) || f[4] != dst {
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

// uidOf returns the account a process runs under, asked of the kernel rather
// than of a program.
//
// It was `ps -o uid=`, one fork on the SYN of every new connection: 1.811ms
// measured, against 11.9µs for this. 152 times, and the forks mattered more than
// the microseconds - fifty simultaneous connections spawned fifty processes at
// once, on the user's machine, for an answer the kernel already had.
//
// kern.proc.pid, and NOT a hand-rolled parse. struct kinfo_proc is in the public
// SDK headers and x/sys generates its Go form from them, so the layout is the
// toolchain's problem rather than something inferred here from a memory dump.
// That distinction is the whole reason this landed and the socket-table half did
// not: xinpcb_n and xsocket_n live in XNU's private headers, and a structure I
// reconstruct by pattern-matching has no business deciding whose traffic this is.
//
// Checked against the thing it replaces before being trusted: every process ps
// could see, 1109 of them, same uid, no disagreement. A pid that has since
// exited returns false, which soleAllows reads as "cannot establish" and lets
// through - exactly what a failed ps did.
// Ucred.Uid is the EFFECTIVE uid, which is what `ps -o uid=` printed and so what
// this replaces exactly. Pcred.P_ruid, the real one, sits beside it and the two
// agree on every ordinary process - no test here can tell them apart, which is
// why the choice is written down instead of left to be re-derived. Effective is
// also the right one: it is what decides what a process may do, and plug's own
// child has had its privilege dropped before it ever opens a socket.
func uidOf(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0, false
	}
	return int(kp.Eproc.Ucred.Uid), true
}

// accountOfPID is uidOf spelled the way the registry spells an account: the
// decimal uid, which is what a client writes in its .uid sidecar
// (thisAccount) and what soleAllows compares against. One spelling on both
// platforms lets the check be written once, with the SID on Windows and the
// uid here, and never have to know which it is holding.
func accountOfPID(pid int) (string, bool) {
	uid, ok := uidOf(pid)
	if !ok {
		return "", false
	}
	return strconv.Itoa(uid), true
}
