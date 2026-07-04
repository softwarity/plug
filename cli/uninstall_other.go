//go:build !darwin && !linux

package main

func uninstall(args []string) {
	info("plug uninstall (the root daemon) is only supported on macOS and Linux")
}
