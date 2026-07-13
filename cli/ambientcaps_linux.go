//go:build linux

package main

import "golang.org/x/sys/unix"

// raiseAmbientCaps promotes plug's file capabilities into the AMBIENT set so
// they survive exec'ing a DOWNLOADED core — a plain binary with no file caps of
// its own. Without this, the launcher-model breaks on Linux the moment the
// cluster runs a different version: the launcher (setcap'd at install) execs
// ~/.plug/versions/<v>/plug, the caps don't cross the exec, and the core dies
// with "plug needs the privileged setup" (caught by the CI launcher-compat
// cell). File caps land in permitted/effective only; ambient requires the cap
// to be in inheritable too, so add each one there first (allowed for any cap
// already in permitted), then raise it ambient. Root needs none of this. The
// mount-ns shim clears the ambient set again before exec'ing the USER's
// command, so no privilege leaks past plug itself. Best-effort: if anything
// fails, the core's own checkPriv reports the plain sudo fallback.
func raiseAmbientCaps() {
	if unix.Geteuid() == 0 {
		return // root: the euid crosses exec by itself
	}
	// CAP_NET_BIND_SERVICE=10, CAP_NET_ADMIN=12, CAP_SYS_ADMIN=21 — the install's
	// setcap trio (see agent/serve-binary and route_linux.go).
	caps := []int{10, 12, 21}

	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return
	}
	for _, c := range caps {
		idx, bit := c>>5, uint32(1)<<(uint(c)&31)
		if data[idx].Permitted&bit != 0 {
			data[idx].Inheritable |= bit
		}
	}
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return
	}
	for _, c := range caps {
		_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(c), 0, 0)
	}
}
