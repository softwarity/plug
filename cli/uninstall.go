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

// uninstall removes everything plug ever installed: every plug binary it can
// locate and the cached versions (profiles in ~/.plug are kept unless the user
// opts to purge them). It runs ROOTLESS — plug is a rootless tool. Only the
// leftovers of the retired root daemon need `sudo plug uninstall`, and only if
// they actually still exist on this machine.
func uninstall(args []string) {
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
	// Guarded like every other privileged touch of a user path, and it was the
	// one place that was not. uninstall runs at euid 0 on macOS and deletes trees
	// derived from realUserHome, which falls back to $HOME: a value the caller
	// sets. Every other site that writes as root under a user path asks this
	// question first (writeProfile, setProfileKey, saveUpdateState, markServed,
	// writeKeyPair, dialTunnel); deleting is not the one operation where it stops
	// mattering.
	guardUserPath(plugDir)
	guardUserPath(filepath.Join(home, ".local", "bin", "plug"))
	if !asked && hasProfiles(plugDir) {
		purge = promptPurge()
	}

	// 1. Legacy root daemon (retired). plug is rootless now, so this only
	// matters for someone who tried the old daemon — and needs root only if the
	// leftovers actually exist.
	cleanupLegacyDaemon()

	// 2. the core store, BEFORE the binaries. Where it belongs to root, plug is
	// the only thing that may delete it — and plug is what we are about to
	// remove. The other order leaves a root-owned directory behind and demands
	// exactly the sudo this whole arrangement exists to avoid.
	for _, d := range []string{versionsDir(), legacyVersionsDir()} {
		if d == "" || d == filepath.Join(plugDir, "versions") {
			continue // that one goes with the config below
		}
		if err := os.RemoveAll(d); err != nil {
			info("could not remove the core store at %s (%v) — remove it by hand", d, err)
		} else if _, err := os.Stat(filepath.Dir(d)); err == nil {
			os.Remove(filepath.Dir(d)) // only if now empty; harmless otherwise
			info("removed the core store at %s", d)
		}
	}

	// 3. every plug binary we can locate
	removeBinaries(home, plugDir)

	// 4. profiles / config
	if purge {
		os.RemoveAll(plugDir)
		info("removed all config in %s", plugDir)
	} else {
		os.RemoveAll(filepath.Join(plugDir, "versions"))
		info("kept your profiles in %s (cache cleared)", plugDir)
	}

	info("plug uninstalled.")
}

// cleanupLegacyDaemon removes the retired root daemon and its files — but only
// if any are actually present, and only escalates when needed. On a normal
// rootless install there is nothing to do, so uninstall needs no root at all.
func cleanupLegacyDaemon() {
	present := false
	for _, f := range append([]string{launchdPlist, systemdUnit}, leftoverFiles...) {
		if _, err := os.Stat(f); err == nil {
			present = true
			break
		}
	}
	if !present {
		return // rootless install — nothing to clean, no sudo needed
	}
	if os.Geteuid() != 0 {
		info("found leftovers from the old root daemon — remove them with:  sudo plug uninstall")
		return
	}
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
	info("removed the old root daemon and its files")
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
