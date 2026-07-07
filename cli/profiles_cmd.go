package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// cmdListProfiles implements `plug ls`: one line per profile with its host:port.
func cmdListProfiles() {
	names := listProfiles()
	if len(names) == 0 {
		fmt.Printf("no profiles yet in %s — create one with 'plug init'\n", plugDir())
		return
	}
	for _, n := range names {
		cfg := loadProfile(n)
		port := cfg.port
		if port == "" {
			port = defaultPort
		}
		fmt.Printf("- %-16s %s:%s\n", n, cfg.host, port)
	}
}

// cmdRemoveProfile implements `plug rm <profile>`.
func cmdRemoveProfile(args []string) {
	if len(args) < 1 {
		fatal("usage: plug rm <profile>")
	}
	name := args[0]
	if err := os.Remove(profilePath(name)); err != nil {
		if os.IsNotExist(err) {
			fatal("no profile %q in %s", name, plugDir())
		}
		fatal("%v", err)
	}
	info("removed profile %q", name)
}

// cmdRenameProfile implements `plug rn <old> <new>` (alias `plug mv`).
func cmdRenameProfile(args []string) {
	if len(args) < 2 {
		fatal("usage: plug rn <old> <new>")
	}
	old, name := args[0], args[1]
	if _, err := os.Stat(profilePath(old)); err != nil {
		fatal("no profile %q in %s", old, plugDir())
	}
	if _, err := os.Stat(profilePath(name)); err == nil {
		fatal("profile %q already exists", name)
	}
	if err := os.Rename(profilePath(old), profilePath(name)); err != nil {
		fatal("%v", err)
	}
	info("renamed profile %q → %q", old, name)
}

// cmdTestProfile implements `plug test [profile]`: check the agent is reachable
// and print its version. With no name, tests the selected/default profile.
func cmdTestProfile(args []string) {
	opts, rest := parseArgs(args)
	var cfg config
	switch {
	case opts.host != "":
		// plug test -H host [--port p]: probe that agent directly, no profile.
		cfg = config{host: opts.host, port: opts.port}
	case opts.profile != "":
		// plug test -p X: probe an existing profile (never creates one).
		cfg = loadProfile(opts.profile)
	case len(rest) >= 1:
		cfg = loadProfile(rest[0]) // plug test <profile-name>
	default:
		cfg = resolveConfig(options{}) // plug test: the default/only profile
	}
	if cfg.host == "" {
		fatal("no host to test")
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	v, err := agentVersion(cfg)
	if err != nil {
		fatal("✗ %s:%s unreachable — %v", cfg.host, cfg.port, err)
	}
	info("✓ %s:%s reachable — agent v%s", cfg.host, cfg.port, v)
}

func profilePath(name string) string {
	return filepath.Join(plugDir(), name+".conf")
}
