//go:build !darwin && !linux

package main

// The native Go tunnel needs a TUN device, available only on darwin/linux.
// On other platforms (native windows) plug runs as a launcher; use WSL2 to run
// the tunnel itself.
func coreRunGo(cfg config, subnets []string, cmdArgs []string) int {
	info("the native Go tunnel is only supported on macOS and Linux (use WSL2 on Windows)")
	return 1
}
