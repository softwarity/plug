//go:build !darwin

package tun

// checkSystemResolver is only meaningful on macOS, where the resolver repoint is
// global (getaddrinfo goes through mDNSResponder). On Linux the child's resolver
// is scoped by its mount namespace — proven by checkLaunchIsolation instead; on
// Windows it is adapter-scoped.
func checkSystemResolver(_ string, _ logfn) error { return nil }
