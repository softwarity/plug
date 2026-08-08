//go:build !linux

package main

import "os"

// execTarget: here the core is named by its path.
//
// Linux has a way to name a file by the descriptor already opened on it, which
// is what its own execTarget uses. macOS dropped that call and Windows never
// had the shape, so this side reaches the same property differently — through
// where the store lives and who may write to it — and that work is tracked
// separately.
//
// What holds on every platform, and is not weakened by this, is that the cached
// core is verified against the digest the agent serves on EVERY launch, before
// anything is run. See openVerified.
func execTarget(f *os.File) (string, []*os.File) {
	return f.Name(), nil
}
