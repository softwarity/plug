//go:build !darwin && !linux

package main

// The daemon mounts a TUN, available only on darwin/linux.
func runDaemon() int {
	info("the plug daemon is only supported on macOS and Linux")
	return 1
}
