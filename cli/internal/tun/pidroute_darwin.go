//go:build darwin

package tun

import (
	"os/exec"
	"strconv"
	"strings"
)

// ppidOf returns pid's parent via `ps -o ppid=`. Cgo-free on purpose — the
// datapath builds with CGO_ENABLED=0, so we avoid libproc. The parent walk is a
// few hops and its result is cached per connection, so the exec cost amortizes.
func ppidOf(pid int) (int, bool) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
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
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-Fpn").Output()
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
