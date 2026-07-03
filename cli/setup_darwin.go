//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const launchdLabel = "io.softwarity.plugd"
const launchdPlist = "/Library/LaunchDaemons/" + launchdLabel + ".plist"

// setup installs (or removes) the root daemon as a launchd LaunchDaemon. Must
// run as root — this is the single sudo the whole tool ever needs.
func setup(args []string) {
	if len(args) > 0 && args[0] == "uninstall" {
		exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
		os.Remove(launchdPlist)
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

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string></array>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>/var/log/plugd.log</string>
  <key>StandardOutPath</key><string>/var/log/plugd.log</string>
</dict>
</plist>
`, launchdLabel, self)

	if err := os.WriteFile(launchdPlist, []byte(plist), 0o644); err != nil {
		fatal("writing %s: %v", launchdPlist, err)
	}
	exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run() // ignore if absent
	if out, err := exec.Command("launchctl", "bootstrap", "system", launchdPlist).CombinedOutput(); err != nil {
		fatal("launchctl bootstrap: %v (%s)", err, out)
	}
	info("plug daemon installed and started — no more sudo needed")
	info("logs: /var/log/plugd.log   ·   remove with: sudo plug setup uninstall")
}

func requireRoot() {
	if os.Geteuid() != 0 {
		fatal("this needs root — run:  sudo plug setup")
	}
}
