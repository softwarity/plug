//go:build windows

package main

import (
	"os"
	"os/exec"
)

// Windows has no setuid: plug elevates per launch (UAC / a SYSTEM service), it
// never runs the child from an inherited root euid, so there is nothing to drop
// and no helper bit to preserve.

func applyPrivDrop(*exec.Cmd) {}

func chownToUser(string) {}

// guardUserPath stays empty here, and the reasoning holds for what it actually
// guards: it is asked about every path a privileged write may touch, directories
// included, and Windows never runs the launcher from an inherited root identity.
//
// The gap is narrower than "privileged writes" and lives elsewhere: the SYSTEM
// service READS a private key whose path came out of a directory the installer
// makes writable by users. That is guardKeyPath, in keypathguard.go, called where
// the key is read rather than on every path in the program. Widening this
// function instead was tried and was wrong: it refused the versions store, a
// directory, for not being a regular file, and would have refused
// %ProgramData%\plug, which plug writes on purpose.
func guardUserPath(string) {}

// readUserOwnedFile is a plain read here. The ownership question on Windows is
// answered by guardKeyOwner, which asks who registered the client rather than
// who owns the file being read, because the account the daemon acts for is not
// the account it runs as.
func readUserOwnedFile(path string) ([]byte, error) { return os.ReadFile(path) }
