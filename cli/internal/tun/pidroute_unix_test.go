//go:build !windows

package tun

import (
	"os"
	"testing"
)

// The unix primitives behind the ancestry walk, asked about this very process.
// On macOS they feed the live router; on Linux they are the reference
// implementation nothing calls yet (see pidroute_linux.go), and this is what
// keeps that implementation true. Windows has its own pair in
// pidroute_windows_test.go.

func TestProcStartSelf(t *testing.T) {
	// The real per-OS procStart must return a positive, readable stamp for us.
	st, ok := procStart(os.Getpid())
	if !ok {
		mustWorkInCI(t, ok, "procStart")
		return
	}
	if st <= 0 {
		t.Fatalf("procStart(self) = %d, want > 0", st)
	}
}

func TestPpidOfSelf(t *testing.T) {
	// The real per-OS ppidOf must agree with the runtime for our own process.
	ppid, ok := ppidOf(os.Getpid())
	if !ok {
		mustWorkInCI(t, ok, "ppidOf")
		return
	}
	if ppid != os.Getppid() {
		t.Fatalf("ppidOf(self) = %d, want %d", ppid, os.Getppid())
	}
}
