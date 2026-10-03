package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"
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

// loadProfile is the entry-point face of the profile reader: a profile that
// cannot be read ends the command. readProfileSoft (doctor.go) is the same
// reader as a value, for doctor, the MCP server and everything that must report
// a broken profile rather than die on it. Both parse through parseProfile, so a
// key one of them knows about is a key both of them know about: the two used to
// be separate loops, and the soft one never learnt `update`, which left the MCP
// tool list_profiles reporting an empty policy for every cluster.
func loadProfile(name string) config {
	data, err := os.ReadFile(profilePath(name))
	if err != nil {
		fatal("profile %q not found in %s", name, plugDir())
	}
	cfg, notes, err := parseProfile(data)
	if err != nil {
		fatal("profile %q: %v", name, err)
	}
	for _, n := range notes {
		info("profile %s: %s", name, n)
	}
	return cfg
}

// parseProfile reads the `key = value` lines of a profile. Blank lines and `#`
// comments are skipped. Keys this version does not know about are ignored, so a
// profile written by a newer plug still loads here; the notes are sentences
// about lines that parse but should be edited out, for the caller to print with
// the profile's name. The error names a line that is none of the three shapes:
// a profile is hand-editable, and a stray line is worth a finding in doctor
// rather than a silent skip.
//
// No defaults are applied here: the port is left empty for the caller to fill
// (resolveConfig and readProfileSoft each have their own moment to do it).
func parseProfile(data []byte) (cfg config, notes []string, err error) {
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			// Said, not refused: a profile is a hand-edited file, and a stray
			// line in it must not stop plug the way a missing key does.
			notes = append(notes, fmt.Sprintf("line %d is not `key = value` and is ignored: %q", i+1, line))
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
			// the SOCKS proxy, and the userspace TUN made that unnecessary: it
			// captures at the IP layer, so `amqp://rabbitmq:5672` works as-is in
			// every runtime. It was parsed and carried to the core and then did
			// NOTHING, which is worse than absent: it read as configured.
			notes = append(notes, "`forward` no longer does anything and can be deleted; "+
				"cluster names work directly now (see the release notes)")
		}
	}
	return cfg, notes, nil
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
			if !strings.EqualFold(prompt(in, name+" already exists, replace its host and port? (y/N)", "n"), "y") {
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

// writeProfile saves host/port into ~/.plug/<name>.conf and returns name. Shared
// by the wizard and the non-interactive `plug -p <name> -H <host> [--port <p>]`.
//
// It EDITS the file rather than rewriting it: a profile also carries the
// personal key `plug keygen` wrote and the update policy `plug config` set, and
// `plug -p prod -H newhost` must not cost either. It used to, silently: the
// next session offered the built-in key and the agent refused someone their
// gateway had enrolled, one hostname change away from anything that mentions
// keys. A file that does not exist yet is created.
func writeProfile(name, host, port string) string {
	path := profilePath(name)
	guardUserPath(path)
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		fatal("%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fatal("cannot read %s: %v", path, err)
	}
	saveProfileText(path, upsertProfileKeys(string(data), [2]string{"host", host}, [2]string{"port", port}))
	info("profile %q saved to %s", name, path)
	return name
}

// upsertProfileKeys rewrites the given keys in a profile's text, in place: the
// first line carrying a key is replaced, a key the file lacks is appended. It
// edits lines rather than reserialising the config, so comments, spacing and
// any key this version does not know about survive being edited by it. The
// one pure step shared by every writer of a profile (writeProfile, setProfileKey).
func upsertProfileKeys(text string, pairs ...[2]string) string {
	lines := strings.Split(text, "\n")
	for _, p := range pairs {
		key, val := p[0], p[1]
		replaced := false
		for i, line := range lines {
			k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && strings.TrimSpace(k) == key {
				lines[i] = fmt.Sprintf("%s = %s", key, val)
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}
		// Append, keeping exactly one trailing newline whatever the file had.
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, fmt.Sprintf("%s = %s", key, val), "")
	}
	return strings.Join(lines, "\n")
}

// saveProfileText is the one write of a profile file: 0600, and handed back to
// the user because the launcher writes it as euid 0 on the setuid path. The
// caller has run guardUserPath on the path.
func saveProfileText(path, text string) {
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		fatal("cannot write %s: %v", path, err)
	}
	chownToUser(path)
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

// openTTY is openTerminal for the wizard and the profile picker, where no
// terminal ends the command: the hint says what to type instead of answering.
func openTTY(hint string) *os.File {
	tty, err := openTerminal()
	if err != nil {
		fatal("no terminal available; %s", hint)
	}
	return tty
}

// openTerminal opens the terminal a question is asked on, whatever stdin was
// redirected to, and is the ONLY place that names the device: /dev/tty on unix,
// CONIN$ on Windows (ttyDevice, per OS). Three prompts opened /dev/tty by its
// literal name and so could never ask anything on Windows: `plug -p new <cmd>`,
// `plug init` and the profile picker all ended with "no terminal available" on
// a perfectly good console.
//
// Being able to OPEN the device is not the test of having a terminal. On
// Windows, CONIN$ opens quite happily in a CI job with no console attached, and
// the read that follows never returns; 16 minutes of a Windows e2e leg went
// that way before askToStop learnt to ask the OS whether stdin IS a terminal.
// Every prompt gets the same guard here, so none of them can wedge a script, a
// CI job or a detached run on a question nobody can answer.
func openTerminal() (*os.File, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, errors.New("stdin is not a terminal")
	}
	return os.Open(ttyDevice)
}
