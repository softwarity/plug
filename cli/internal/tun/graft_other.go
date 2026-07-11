//go:build !darwin && !windows

package tun

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
