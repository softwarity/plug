package main

import (
	"runtime"

	"github.com/softwarity/plug/cli/internal/tun"
)

// runSelfTest exercises the whole TUN data path on THIS OS with no agent and no
// Docker — it loops traffic through a real device by name — and prints a stable
// PASS/FAIL line. CI runs it on macOS/Windows/Linux runners as the native,
// visible proof that the privileged path works on each platform.
func runSelfTest() int {
	if !tun.Available() {
		info("SELFTEST-SKIP: TUN not available on %s", runtime.GOOS)
		return 0
	}
	if err := tun.SelfTest(info); err != nil {
		info("SELFTEST-FAIL: %v", err)
		return 1
	}
	info("SELFTEST-OK: tun datapath reachable by name")
	return 0
}
