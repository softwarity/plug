package main

import (
	"bufio"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// Legacy paths from the retired root-daemon design. `plug uninstall` removes
// them so anyone who tried the daemon can get back to a clean, rootless setup.
const (
	launchdLabel = "io.softwarity.plugd"
	launchdPlist = "/Library/LaunchDaemons/io.softwarity.plugd.plist"
	systemdUnit  = "/etc/systemd/system/plugd.service"
)

var leftoverFiles = []string{
	"/var/run/plug.sock",
	"/var/log/plugd.log",
	"/var/run/plugd-dns.json",
	"/var/run/plugd-resolv.backup",
}

// uninstall removes everything plug ever installed: the (now retired) root
// daemon, its files, every plug binary it can locate, and the cached versions.
// Profiles in ~/.plug are kept unless the user opts to purge them. Needs root
// to remove the daemon — sudo plug uninstall.
func uninstall(args []string) {
	if os.Geteuid() != 0 {
		fatal("this needs root — run:  sudo plug uninstall")
	}

	purge, asked := false, false
	for _, a := range args {
		switch a {
		case "--purge":
			purge, asked = true, true
		case "--keep-config", "--keep":
			purge, asked = false, true
		}
	}

	home := realUserHome()
	plugDir := filepath.Join(home, ".plug")
	if !asked && hasProfiles(plugDir) {
		purge = promptPurge()
	}

	// 1. retired root daemon + its files
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
		os.Remove(launchdPlist)
	case "linux":
		exec.Command("systemctl", "disable", "--now", "plugd").Run()
		os.Remove(systemdUnit)
		exec.Command("systemctl", "daemon-reload").Run()
	}
	for _, f := range leftoverFiles {
		os.Remove(f)
	}
	info("removed the root daemon and its files")

	// 2. every plug binary we can locate
	removeBinaries(home, plugDir)

	// 3. profiles / config
	if purge {
		os.RemoveAll(plugDir)
		info("removed all config in %s", plugDir)
	} else {
		os.RemoveAll(filepath.Join(plugDir, "versions"))
		info("kept your profiles in %s (cache cleared)", plugDir)
	}

	info("plug uninstalled.")
}

func removeBinaries(home, plugDir string) {
	seen := map[string]bool{}
	candidates := []string{
		filepath.Join(home, ".local", "bin", "plug"),
		"/usr/local/bin/plug",
		"/opt/homebrew/bin/plug",
	}
	if self, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(self); err == nil {
			candidates = append(candidates, r)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(plugDir, "versions")); err == nil {
		for _, e := range entries {
			candidates = append(candidates, filepath.Join(plugDir, "versions", e.Name(), "plug"))
		}
	}
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err == nil {
			if os.Remove(p) == nil {
				info("removed %s", p)
			}
		}
	}
}

func hasProfiles(plugDir string) bool {
	entries, err := os.ReadDir(plugDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".conf") {
			return true
		}
	}
	return false
}

func promptPurge() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		info("keeping your profiles (no terminal to ask; use --purge to remove)")
		return false
	}
	defer tty.Close()
	ans := prompt(bufio.NewReader(tty), "Also delete your saved profiles in ~/.plug? (y/N)", "n")
	return strings.EqualFold(ans, "y")
}

// realUserHome resolves the invoking user's home even under sudo (SUDO_USER).
func realUserHome() string {
	if su := os.Getenv("SUDO_USER"); su != "" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			return u.HomeDir
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
