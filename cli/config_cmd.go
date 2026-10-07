package main

import (
	"fmt"
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

	if !updatesOffered() {
		// The one setting here is about `plug update`, which a hosted plug does
		// not carry: showing a mode, or taking one, would promise what it
		// cannot do.
		if setting != "" {
			refuseVerb("config update", "the gateway that serves this plug decides its version, and its agent's")
		}
		fmt.Printf("- update    decided by the gateway that serves this plug\n")
		fmt.Printf("\nprofile %q, stored in %s\n", name, profilePath(name))
		return
	}
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

// setProfileKey rewrites one key in an EXISTING profile, in place (see
// upsertProfileKeys: comments, spacing and unknown keys survive). Unlike
// writeProfile it refuses to create the file: a key or an update policy with no
// host to go with it is not a profile anyone can use.
func setProfileKey(name, key, val string) {
	editProfile(name, false, func(text string) string {
		return upsertProfileKeys(text, [2]string{key, val})
	})
}
