package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- profiles ----

func resolveConfig(o options) config {
	var cfg config
	switch {
	case o.profile != "" && o.host != "":
		// -p X -H host [--port p]: (re)define profile X from these, then use it —
		// "set it and run" in one line, no wizard.
		port := o.port
		if port == "" {
			port = defaultPort
		}
		writeProfile(o.profile, o.host, port)
		cfg = config{host: o.host, port: port}
	case o.host != "":
	case o.profile != "":
		// An unknown -p profile isn't an error: offer the wizard to create it, so
		// reaching a new cluster is just `plug -p <newname> <cmd>` (no re-install).
		if _, err := os.Stat(profilePath(o.profile)); err != nil {
			info("profile %q doesn't exist yet — let's create it", o.profile)
			cfg = loadProfile(wizard(o.profile, false))
		} else {
			cfg = loadProfile(o.profile)
		}
	default:
		names := listProfiles()
		switch len(names) {
		case 0:
			info("no profile in %s — let's create one", plugDir())
			cfg = loadProfile(wizard("default", false))
		case 1:
			info("using profile %q", names[0])
			cfg = loadProfile(names[0])
		default:
			cfg = loadProfile(chooseProfile(names))
		}
	}
	if o.host != "" {
		cfg.host = o.host
	}
	if o.port != "" {
		cfg.port = o.port
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	return cfg
}

func plugDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("%v", err)
	}
	return filepath.Join(home, ".plug")
}

func listProfiles() []string {
	entries, err := os.ReadDir(plugDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
			names = append(names, strings.TrimSuffix(e.Name(), ".conf"))
		}
	}
	sort.Strings(names)
	return names
}

func loadProfile(name string) config {
	var cfg config
	data, err := os.ReadFile(profilePath(name))
	if err != nil {
		fatal("profile %q not found in %s", name, plugDir())
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "host":
			cfg.host = val
		case "port":
			cfg.port = val
		case "update":
			cfg.updateMode = val
		case "key":
			cfg.key = val
		case "forward":
			// Removed: it declared a local port-forward for drivers that ignored
			// the SOCKS proxy, and the userspace TUN made that unnecessary — it
			// captures at the IP layer, so `amqp://rabbitmq:5672` works as-is in
			// every runtime. It was parsed and carried to the core and then did
			// NOTHING, which is worse than absent: it read as configured.
			info("profile %s: `forward` no longer does anything and can be deleted — "+
				"cluster names work directly now (see the release notes)", name)
		}
	}
	return cfg
}

func chooseProfile(names []string) string {
	tty := openTTY("several profiles found, pick one with -p <name>")
	defer tty.Close()
	info("several profiles found:")
	for i, n := range names {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, n)
	}
	in := bufio.NewReader(tty)
	for {
		fmt.Fprintf(os.Stderr, "choose [1-%d]: ", len(names))
		line, err := in.ReadString('\n')
		if err != nil {
			fatal("aborted")
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= len(names) {
			return names[n-1]
		}
	}
}

func wizard(defaultName string, confirmOverwrite bool) string {
	tty := openTTY("cannot run the profile wizard; use --host instead")
	defer tty.Close()
	in := bufio.NewReader(tty)

	name := prompt(in, "profile name", defaultName)
	path := profilePath(name)
	if confirmOverwrite {
		if _, err := os.Stat(path); err == nil {
			if !strings.EqualFold(prompt(in, name+" already exists, overwrite? (y/N)", "n"), "y") {
				fatal("aborted")
			}
		}
	}
	var host string
	for host == "" {
		host = prompt(in, "cluster host", "")
	}
	port := prompt(in, "agent port", defaultPort)

	return writeProfile(name, host, port)
}

// writeProfile saves ~/.plug/<name>.conf with host/port and returns name. Shared
// by the wizard and the non-interactive `plug -p <name> -H <host> [--port <p>]`.
func writeProfile(name, host, port string) string {
	path := profilePath(name)
	guardUserPath(path)
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		fatal("%v", err)
	}
	content := fmt.Sprintf("host = %s\nport = %s\n", host, port)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fatal("%v", err)
	}
	info("profile %q saved to %s", name, path)
	return name
}

func initProfile() {
	name := wizard("default", true)
	info("try it:  plug -p %s <your command>", name)
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, err := in.ReadString('\n')
	if err != nil {
		fatal("aborted")
	}
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func openTTY(hint string) *os.File {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		fatal("no terminal available — %s", hint)
	}
	return tty
}
