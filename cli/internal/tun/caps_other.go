//go:build !linux

package tun

import "os/exec"

// withPrivCaps is a no-op off Linux: macOS/Windows configure runs under sudo /
// admin, so its helpers are already privileged.
func withPrivCaps(_ *exec.Cmd) {}
