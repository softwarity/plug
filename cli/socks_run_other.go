//go:build !darwin && !windows

package main

// coreRun off macOS: each launch is autonomous — it dials its own tunnel and holds
// its own datapath for the child's lifetime. On Linux the child's resolver is
// scoped by its mount namespace, so concurrent launches never collide; no daemon
// is needed (restarting one process doesn't touch the others).
func coreRun(cfg config, cmdArgs []string) int {
	return runCoreInProcess(cfg, cmdArgs)
}
