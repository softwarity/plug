package main

import "errors"

// Windows has the SMB client of them all, but its redirector speaks to port
// 445 only - a UNC path carries no port - and the system's own server holds
// that port on every address. The forward has to sit on 445 of an address the
// system does not serve (a loopback alias, or plug's own TUN address), which
// is the half not written yet. Until then --mount says so here instead of
// failing later with a message about a share.

func mountSupported() error {
	return errors.New("--mount is not available on Windows yet (the SMB redirector needs the forward on port 445 of a dedicated address)")
}

func mountBindAddr() string { return "127.0.0.1:0" }

func mountSMB(local, share, user, pass, path string) error { return mountSupported() }

func unmountSMB(path string) error { return nil }

func mountedAt(path string) bool { return false }
