//go:build !windows

package main

import "path/filepath"

// What macOS and Linux share about mounting: the volume is reached through a
// forward this process keeps open, lands in a directory, and no name has to
// be told to the machine's datapath. The OS-specific part, the mount itself,
// is in mount_darwin.go and mount_linux.go.

func mountSupported() error { return nil }

func mountUsesForward() bool { return true }

// autoMountPath is where an automatic mount goes: under the session
// directory, at its cluster path.
func autoMountPath(dir, clusterPath string) string {
	return filepath.Join(dir, filepath.FromSlash(clusterPath))
}

// pinMountName / unpinMountNames: nothing to pin here. The helper's name is
// dialled by this process through its own forward, never by the kernel
// through the machine's datapath (see mount_windows.go).
func pinMountName(config, string) {}

func unpinMountNames(config) {}
