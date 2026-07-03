//go:build darwin

package tunnel

import (
	"fmt"
	"os/exec"
)

// macOS names utun devices automatically; wireguard/tun picks the next free
// one from this prefix and Device.Name() returns the real name (utunN).
const tunDevName = "utun"

func configureInterface(name, addr string, subnets []string) error {
	// point-to-point address on the utun, then a host route per cluster subnet.
	if out, err := exec.Command("ifconfig", name, addr, addr, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig %s: %v (%s)", name, err, out)
	}
	for _, s := range subnets {
		if out, err := exec.Command("route", "-q", "-n", "add", "-net", s, "-interface", name).CombinedOutput(); err != nil {
			return fmt.Errorf("route add %s: %v (%s)", s, err, out)
		}
	}
	return nil
}
