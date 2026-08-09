//go:build !darwin

package main

import "path/filepath"

// versionsDir: the cached cores stay under the user's own tree here.
//
// On Linux the launcher holds file capabilities, not root — cap_net_admin,
// cap_sys_admin, cap_net_bind_service, and neither cap_dac_override nor
// cap_chown — so it could not write a store owned by root without the install
// granting far more than it does today. It does not need to: the core is
// executed through the descriptor plug verified, not through its path, so what
// the store holds a moment later never runs (see coreexec_linux.go).
//
// On Windows the launcher is not elevated at all — it elevates per launch
// through the SYSTEM service, and the service runs the INSTALLED launcher, not
// a cached core. Nothing here ever runs with privilege the user does not
// already have.
var versionsDir = func() string { return filepath.Join(plugDir(), "versions") }

// legacyVersionsDir: nothing moved on these platforms, so there is no older
// place to clean. Empty means "nothing to do", which is what the callers read.
func legacyVersionsDir() string { return "" }

func storeIsSystem() bool { return false }

// guardStorePath: the store is inside the user's own tree, so the guard is the
// existing one — refuse to write there AS ROOT if the path resolves outside it.
func guardStorePath(path string) { guardUserPath(path) }
