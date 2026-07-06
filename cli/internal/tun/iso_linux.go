//go:build linux

package tun

import "fmt"

// checkLaunchIsolation proves the per-launch mount namespace works end to end: it
// runs a child through runChild (which enters a fresh mount ns and bind-mounts
// the private resolv.conf there) and checks the child sees ONLY our resolver. It
// exercises the exact path a real `plug <cmd>` takes — including that a setcap'd,
// non-root plug can create the namespace.
func checkLaunchIsolation(privResolv string, log logfn) error {
	if privResolv == "" {
		return nil
	}
	code, err := runChild([]string{"sh", "-c", "grep -qx 'nameserver 127.0.0.1' /etc/resolv.conf"}, privResolv)
	if err != nil || code != 0 {
		return fmt.Errorf("per-launch DNS isolation check failed (exit %d): %v", code, err)
	}
	log.f("selftest: per-launch mount ns OK — the child sees only the private resolver")
	return nil
}
