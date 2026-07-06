//go:build !linux

package tun

// checkLaunchIsolation is a no-op off Linux (no mount namespaces; the resolver is
// repointed globally by configure instead).
func checkLaunchIsolation(_ string, _ logfn) error { return nil }
