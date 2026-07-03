//go:build !darwin && !linux

package main

func setup(args []string) {
	info("plug setup (the root daemon) is only supported on macOS and Linux")
}
