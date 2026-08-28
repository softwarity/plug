//go:build !windows

package tun

import (
	"os"
	"path/filepath"
	"strings"
)

// Where a privileged helper may come from, and nowhere else.
//
// run() hands plug's capabilities to the process it starts (withPrivCaps), and
// every caller names its helper bare: ip, sysctl, ifconfig, route, scutil. Bare
// means resolved through the CALLER's $PATH, so a `PATH=/tmp/evil:$PATH` holding
// a fake `ip` is a fake `ip` running with CAP_SYS_ADMIN, or as root on macOS.
//
// The launcher narrows $PATH for exactly this reason (securePath), but that
// narrowing only fires when euid differs from ruid - which is true of the setuid
// macOS install and FALSE of Linux file capabilities, the one platform its own
// comment names. So on Linux the hole was open the whole time.
//
// This closes it where the exec happens rather than by fixing the guard's
// condition: the helper is looked up HERE, in root-owned system directories, and
// $PATH is never consulted. It holds whatever euid plug is running under.
//
// The list is not a hardcoded /sbin/ip. NixOS has no /sbin at all, and pinning
// one layout would break those machines; these are the root-owned directories
// where system tooling lives across mainstream distros, macOS and NixOS. Same
// list the launcher narrows to, for the same reason.
var helperDirs = []string{
	"/usr/sbin", "/usr/bin", "/sbin", "/bin",
	"/run/current-system/sw/bin", "/run/wrappers/bin",
}

// helperPath resolves a privileged helper to an absolute path under helperDirs.
// An absolute name is taken as given: the caller already said where it wants to
// go, and second-guessing it would break an operator who moved a tool on purpose.
func helperPath(bin string) (string, bool) {
	if strings.ContainsRune(bin, os.PathSeparator) {
		return bin, true
	}
	for _, dir := range helperDirs {
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return bin, false
}

// helperDirsList is for the error a failed lookup produces: someone on a layout
// nobody anticipated needs to see where plug looked before they can say why.
func helperDirsList() string { return strings.Join(helperDirs, ", ") }

// HelperPath is helperPath for the launcher and the daemon, which run the same
// class of tool (scutil, ps) from package main while holding the same privilege.
// Falls back to the bare name when nothing is found, because those callers read
// output rather than change the system: losing the reading is worse than reading
// one produced by something on $PATH, and the caller that CHANGES the machine
// (run) refuses instead.
func HelperPath(bin string) string {
	p, _ := helperPath(bin)
	return p
}
