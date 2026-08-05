//go:build !linux && !darwin && !windows

package tun

import (
	"fmt"
	"runtime"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return false }

const defaultTUNName = ""

const maxInstances = 1

func tunNameFor(int) string { return defaultTUNName }

func checkPriv() error {
	return fmt.Errorf("plug --tun is not available on %s", runtime.GOOS)
}

func configure(_ any, _ int, _, _, _ string, _ *upstreamDNS, _ logfn) ([]string, string, func(), error) {
	return nil, "", func() {}, fmt.Errorf("plug --tun is not available on %s", runtime.GOOS)
}
