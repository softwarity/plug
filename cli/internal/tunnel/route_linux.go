//go:build linux

package tunnel

import (
	"fmt"
	"os/exec"
)

const tunDevName = "plug0"

func configureInterface(name, addr string, subnets []string) error {
	steps := [][]string{
		{"ip", "addr", "add", addr + "/32", "dev", name},
		{"ip", "link", "set", "dev", name, "up"},
	}
	for _, s := range subnets {
		steps = append(steps, []string{"ip", "route", "add", s, "dev", name})
	}
	for _, c := range steps {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %v (%s)", c, err, out)
		}
	}
	return nil
}
