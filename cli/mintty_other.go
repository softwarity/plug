//go:build !windows

package main

import "os"

// minttyInput is Windows-only: a unix terminal is /dev/tty, whatever emulates it.
func minttyInput() *os.File { return nil }
