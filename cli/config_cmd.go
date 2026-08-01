package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Settings live in ~/.plug/config, next to the profiles but deliberately not one
// of them: a profile describes a CLUSTER, and there is one per cluster, whereas
// these describe THIS MACHINE. Update policy belongs to the machine — the same
// launcher serves every cluster, and it is the launcher that gets replaced.
//
// listProfiles only picks up "*.conf", so this file is never mistaken for one.
func settingsPath() string { return filepath.Join(plugDir(), "config") }

// updateMode values. notify is the default: plug says a newer version is there
// and leaves the decision alone. Nothing is ever replaced without the user
// having asked for it once.
const (
	updateNone   = "none"
	updateNotify = "notify"
	updateAuto   = "auto"
)

var updateModes = []string{updateNone, updateNotify, updateAuto}

// settingKeys is the whole surface. Adding to it is a product decision, not a
// convenience — every key here is one more thing to explain and to keep working.
var settingKeys = map[string][]string{
	"update": updateModes,
}

func loadSettings() map[string]string {
	s := map[string]string{}
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			s[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return s
}

func saveSettings(s map[string]string) error {
	var b strings.Builder
	b.WriteString("# plug machine settings — see `plug config`\n")
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, s[k])
	}
	path := settingsPath()
	guardUserPath(path) // plug may hold root here — never write outside the caller's tree
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	// Written as euid 0 on the setuid path, which would leave it root-owned and
	// un-editable without sudo — the same trap already fixed for the core cache.
	chownToUser(path)
	return nil
}

// updateMode is the effective policy, defaulting to notify.
func updateMode() string {
	switch v := loadSettings()["update"]; v {
	case updateNone, updateNotify, updateAuto:
		return v
	default:
		return updateNotify
	}
}

// cmdConfig implements `plug config` (show), `plug config <key>` (read) and
// `plug config <key>=<value>` (write).
func cmdConfig(args []string) {
	if len(args) == 0 {
		s := loadSettings()
		keys := make([]string, 0, len(settingKeys))
		for k := range settingKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, set := s[k]
			if !set {
				val = defaultFor(k) + "  (default)"
			}
			fmt.Printf("- %-10s %s\n", k, val)
			fmt.Printf("  %s\n", strings.Join(settingKeys[k], " | "))
		}
		fmt.Printf("\nstored in %s\n", settingsPath())
		return
	}

	key, val, assigning := strings.Cut(args[0], "=")
	key = strings.TrimSpace(key)
	allowed, known := settingKeys[key]
	if !known {
		fatal("unknown setting %q — plug config knows: %s", key, strings.Join(keysOf(settingKeys), ", "))
	}
	if !assigning {
		s := loadSettings()
		if v, ok := s[key]; ok {
			fmt.Println(v)
		} else {
			fmt.Println(defaultFor(key))
		}
		return
	}

	val = strings.TrimSpace(val)
	if !slices.Contains(allowed, val) {
		fatal("%s=%q is not one of: %s", key, val, strings.Join(allowed, ", "))
	}
	s := loadSettings()
	s[key] = val
	if err := saveSettings(s); err != nil {
		fatal("cannot write %s: %v", settingsPath(), err)
	}
	info("%s=%s", key, val)
}

func defaultFor(key string) string {
	if key == "update" {
		return updateNotify
	}
	return ""
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
