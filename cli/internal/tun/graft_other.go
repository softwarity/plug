//go:build !darwin && !windows

package tun

// graftDir is where the datapath publishes state readable from outside the
// process. On Linux there is no shared daemon to graft onto — each launch is
// autonomous — but `plug doctor` still wants to know which servers a running
// session forwards to, and this is where that is written. Best-effort: a
// session that cannot create it simply publishes nothing, and doctor says so.
var graftDir = "/var/run/plug" // overridable in tests

// AcquireCluster is leader-only off macOS. On Linux each launch is autonomous —
// its mount namespace isolates its private resolv.conf, so several `plug`s of the
// same cluster already coexist without coordination. On Windows the DNS is
// per-adapter; per-cluster grafting there is a TODO, so for now each launch
// behaves as its own leader.
func AcquireCluster(_ string) (leader bool, release func(), err error) {
	return true, func() {}, nil
}

// SharedKnownHosts is Windows-only (its SYSTEM service needs a user-writable TOFU
// file). Elsewhere dialTunnel pins under the user's ~/.plug, so this returns "".
func SharedKnownHosts() string { return "" }
