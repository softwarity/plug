//go:build !darwin && !windows

package main

// daemonMain is macOS-only: the persistent datapath daemon exists because macOS
// has a global system resolver. On Linux/Windows each `plug` is autonomous (its
// own mount namespace / adapter), so this verb is never dispatched there.
func daemonMain(_ []string) int { return 1 }

// cmdDown is a no-op off macOS: there is no daemon (each launch is autonomous).
func cmdDown(_ []string) {
	info("plug has no daemon on this OS — each launch is autonomous")
}
