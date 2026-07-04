//go:build darwin || linux

package main

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const daemonLog = "/var/log/plugd.log"

// uninstall removes everything plug installed: the root daemon, the socket and
// log, every plug binary it knows about, and the cached versions. Profiles in
// ~/.plug are kept unless the user opts to purge them. Must run as root (to
// remove the daemon) — sudo plug uninstall.
func uninstall(args []string) {
	requireRoot()

	// --purge / --keep-config skip the prompt (for scripts).
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

	// 1. daemon + its runtime files
	uninstallDaemon()
	os.Remove(socketPath)
	os.Remove(daemonLog)
	info("removed daemon, socket and log")

	// 2. every plug binary we can locate (self + standard install spots + cache)
	removeBinaries(home, plugDir)

	// 3. profiles / config
	if purge {
		os.RemoveAll(plugDir)
		info("removed all config in %s", plugDir)
	} else {
		os.RemoveAll(filepath.Join(plugDir, "versions")) // cache is not config
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
	// cached launcher versions (~/.plug/versions/<v>/plug)
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

// realUserHome resolves the invoking user's home even under sudo (SUDO_USER),
// so we clean ~/.plug of the actual user, not root.
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
