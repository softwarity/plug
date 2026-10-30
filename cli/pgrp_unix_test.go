//go:build !windows

package main

import "syscall"

// processGroup is the fake docker's process group, compared with the test's.
func processGroup() int { return syscall.Getpgrp() }
