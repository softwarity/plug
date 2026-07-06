//go:build !linux

package tun

import (
	"fmt"
	"runtime"
)

// NsShimMain is Linux-only; the mount-namespace re-exec is never spawned off
// Linux, but the symbol must exist so main() compiles on every OS.
func NsShimMain(_ []string) error {
	return fmt.Errorf("%s is Linux-only (%s)", NsShimVerb, runtime.GOOS)
}
