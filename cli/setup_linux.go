//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const systemdUnit = "/etc/systemd/system/plugd.service"

// setup installs (or removes) the root daemon as a systemd service. Must run as
// root — the single sudo the whole tool ever needs.
func setup(args []string) {
	if len(args) > 0 && args[0] == "uninstall" {
		exec.Command("systemctl", "disable", "--now", "plugd").Run()
		os.Remove(systemdUnit)
		exec.Command("systemctl", "daemon-reload").Run()
		os.Remove(socketPath)
		info("plug daemon uninstalled")
		return
	}
	requireRoot()

	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	unit := fmt.Sprintf(`[Unit]
Description=plug tunnel helper
After=network.target

[Service]
ExecStart=%s daemon
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, self)

	if err := os.WriteFile(systemdUnit, []byte(unit), 0o644); err != nil {
		fatal("writing %s: %v", systemdUnit, err)
	}
	exec.Command("systemctl", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "enable", "--now", "plugd").CombinedOutput(); err != nil {
		fatal("systemctl enable: %v (%s)", err, out)
	}
	info("plug daemon installed and started — no more sudo needed")
	info("logs: journalctl -u plugd   ·   remove with: sudo plug setup uninstall")
}

func requireRoot() {
	if os.Geteuid() != 0 {
		fatal("this needs root — run:  sudo plug setup")
	}
}
