package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// The update policy is a property of the CLUSTER, not of this machine: `auto`
// updates the AGENT, and an agent is shared. You may well govern your own local
// cluster and have no say at all over the shared one — so the setting lives in
// the profile, beside host and port, and is set per profile.
const (
	updateNone   = "none"
	updateNotify = "notify"
	updateAuto   = "auto"
)

var updateModes = []string{updateNone, updateNotify, updateAuto}

// normalizeUpdateMode maps anything unrecognised onto the default. A value that
// is not one of the three is a profile someone hand-edited; guessing what they
// meant would be worse than the documented default.
func normalizeUpdateMode(v string) string {
	if slices.Contains(updateModes, v) {
		return v
	}
	return updateNotify
}

// cmdConfig implements `plug config` (show) and `plug config update=<mode>`
// (set), on the profile named the same way every other subcommand names one.
func cmdConfig(args []string) {
	var profile, setting string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			profile = flagValue(args, &i)
		default:
			if strings.HasPrefix(args[i], "-") || setting != "" {
				fatal("usage: plug config [-p profile] [update=%s]", strings.Join(updateModes, "|"))
			}
			setting = args[i]
		}
	}
	name := configTarget(profile)
	cfg := loadProfile(name)

	if setting == "" {
		fmt.Printf("- update    %s\n", cfg.updateMode)
		fmt.Printf("  %s\n", strings.Join(updateModes, " | "))
		fmt.Printf("\nprofile %q, stored in %s\n", name, profilePath(name))
		return
	}

	key, val, assigning := strings.Cut(setting, "=")
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	if key != "update" {
		fatal("unknown setting %q — plug config knows: update", key)
	}
	if !assigning {
		fmt.Println(cfg.updateMode)
		return
	}
	if !slices.Contains(updateModes, val) {
		fatal("update=%q is not one of: %s", val, strings.Join(updateModes, ", "))
	}
	setProfileKey(name, "update", val)
	info("profile %q: update=%s", name, val)
}

// configTarget names the profile to read or write. Unlike `plug update` there is
// no -H form: a host with no profile has nowhere to keep a setting.
func configTarget(profile string) string {
	if profile != "" {
		return profile
	}
	names := listProfiles()
	switch len(names) {
	case 0:
		fatal("no profile configured — create one with 'plug init'")
	case 1:
		return names[0]
	default:
		fatal("several profiles (%s) — name the cluster: plug config -p <name> …", strings.Join(names, ", "))
	}
	return ""
}

// setProfileKey rewrites one key in a profile, in place. It reads the file back
// line by line rather than reserialising it, so comments, spacing and any key
// this version does not know about survive being edited by it.
func setProfileKey(name, key, val string) {
	path := profilePath(name)
	guardUserPath(path) // plug may hold root here — never write outside the caller's tree
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("no profile %q in %s — create one with 'plug init'", name, plugDir())
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == key {
			lines[i] = fmt.Sprintf("%s = %s", key, val)
			replaced = true
			break
		}
	}
	if !replaced {
		// Append, keeping exactly one trailing newline whatever the file had.
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, fmt.Sprintf("%s = %s", key, val), "")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		fatal("cannot write %s: %v", path, err)
	}
	chownToUser(path) // written as euid 0 on the setuid path
}
