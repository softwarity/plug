//go:build !linux

package main

// raiseAmbientCaps is Linux-only: macOS transmits privilege through the setuid
// euid (crosses exec by itself) and Windows holds it in the SYSTEM service.
func raiseAmbientCaps() {}
