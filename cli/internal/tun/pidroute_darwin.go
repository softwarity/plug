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
// lsof (cgo-free; the datapath builds with CGO_ENABLED=0). `-nP` keeps it numeric
// and fast; `-Fpn` emits a machine-readable stream — one `pPID` line per process
// followed by its `nLOCAL->REMOTE` socket lines — so we scan for the socket whose
// LOCAL endpoint ends in :srcPort and return the PID that owns it. The daemon runs
// as root, so it sees every process's sockets.
func pidForLocalPort(srcPort uint16) (int, bool) {
	out, err := exec.Command(HelperPath("lsof"), "-nP", "-iTCP", "-Fpn").Output()
	if err != nil {
		return 0, false
	}
	want := ":" + strconv.Itoa(int(srcPort))
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			local := line[1:]
			if i := strings.Index(local, "->"); i >= 0 {
				local = local[:i] // keep the LOCAL endpoint, drop ->REMOTE
			}
			if pid != 0 && strings.HasSuffix(local, want) {
				return pid, true
			}
		}
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
