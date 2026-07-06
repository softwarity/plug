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

// pidForLocalPort is not yet implemented on macOS. The connect-time
// source-port → PID lookup needs the OS TCP table; the cgo-free options are
// `lsof -nP -iTCP:<port>` (parse the PID column) or the sysctl
// net.inet.tcp.pcblist. Wired when the N-tunnel daemon lands (docs/multicluster.md).
// Refusing here is correct: the single-cluster path never calls this.
func pidForLocalPort(uint16) (int, bool) { return 0, false }
