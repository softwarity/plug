//go:build windows

package main

import "os/exec"

// Windows has no setuid: plug elevates per launch (UAC / a SYSTEM service), it
// never runs the child from an inherited root euid, so there is nothing to drop
// and no helper bit to preserve.

func applyPrivDrop(*exec.Cmd) {}

func chownToUser(string) {}

// Windows never runs plug from an inherited root euid (it elevates per launch),
// so there is no privileged write to guard.
func guardUserPath(string) {}
