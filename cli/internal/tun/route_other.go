//go:build !linux && !darwin && !windows

package tun

import (
	"fmt"
	"runtime"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return false }

const defaultTUNName = ""

func checkPriv() error {
	return fmt.Errorf("plug --tun is not available on %s", runtime.GOOS)
}

func configure(_ any, _, _ string, _ logfn) ([]string, string, func(), error) {
	return nil, "", func() {}, fmt.Errorf("plug --tun is not available on %s", runtime.GOOS)
}
