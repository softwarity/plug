package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/term"
)

// resolveConfig is where -p, -H and --port become the cluster a session
// dials. Each case here is one way a person runs plug; the two that end in
// a question (several profiles, a profile to create) are the ones a script
// or a CI job hits, and what they are told is the whole test.

// noTerminal is what the stand-in fatal panics with, so the refusal can be
// unwound and read instead of ending the test binary.
type noTerminal string

// noTerminalRefusal runs fn with openTTY's fatal standing in a panic and
// returns what it said, "" when no terminal was asked for. The real one exits
// the test binary, so this is the only way to read the refusal.
func noTerminalRefusal(t *testing.T, fn func()) (msg string) {
	t.Helper()
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stderr.Fd())) {
		t.Skip("stdin or stderr is a terminal here; the picker would ask for real")
	}
	saved := noTerminalFatal
	t.Cleanup(func() { noTerminalFatal = saved })
	noTerminalFatal = func(format string, a ...any) { panic(noTerminal(fmt.Sprintf(format, a...))) }
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if got, ok := r.(noTerminal); ok {
			msg = string(got)
			return
		}
		panic(r)
	}()
	fn()
	return ""
}

func writeTestProfile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath(name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// -H alone dials that host, on --port or the default; no profile is touched.
func TestResolveConfigHostAlone(t *testing.T) {
	sandboxHome(t)
	cfg := resolveConfig(options{host: "h.example"})
	if cfg.host != "h.example" || cfg.port != defaultPort {
		t.Errorf("-H h.example resolved to %+v", cfg)
	}
	cfg = resolveConfig(options{host: "h.example", port: "2200"})
	if cfg.host != "h.example" || cfg.port != "2200" {
		t.Errorf("-H h.example --port 2200 resolved to %+v", cfg)
	}
	if names := listProfiles(); len(names) != 0 {
		t.Errorf("-H alone wrote a profile: %v", names)
	}
}

// -p X -H host defines (or redefines) X and uses it: set it and run, in one
// line. The existing key and update policy of X stay (writeProfile's job,
// proven on its own; here, that writeProfile is what got called).
func TestResolveConfigProfileWithHostRedefinesIt(t *testing.T) {
	sandboxHome(t)
	keyPath := filepath.Join(plugDir(), "keys", "prod")
	writeTestProfile(t, "prod", "host = old.example\nport = 2222\nkey = "+keyPath+"\nupdate = auto\n")
	cfg := resolveConfig(options{profile: "prod", host: "new.example", port: "2200"})
	if cfg.host != "new.example" || cfg.port != "2200" {
		t.Errorf("resolved to %+v", cfg)
	}
	after, err := os.ReadFile(profilePath("prod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host = new.example\n", "port = 2200\n", "key = " + keyPath + "\n", "update = auto\n"} {
		if !strings.Contains(string(after), want) {
			t.Errorf("the profile lacks %q after -p prod -H new.example:\n%s", strings.TrimSpace(want), after)
		}
	}
	// Without --port the default is written, not an empty line.
	sandboxHome(t)
	resolveConfig(options{profile: "fresh", host: "h.example"})
	got, _ := os.ReadFile(profilePath("fresh"))
	if string(got) != "host = h.example\nport = "+defaultPort+"\n" {
		t.Errorf("-p fresh -H h.example wrote %q", got)
	}
}

// -p X on an existing profile loads it whole; --port overrides its port for
// this run without rewriting the file.
func TestResolveConfigLoadsAnExistingProfile(t *testing.T) {
	sandboxHome(t)
	keyPath := filepath.Join(plugDir(), "keys", "prod")
	writeTestProfile(t, "prod", "host = prod.example\nport = 2300\nkey = "+keyPath+"\nupdate = notify\n")
	cfg := resolveConfig(options{profile: "prod"})
	if cfg.host != "prod.example" || cfg.port != "2300" || cfg.key != keyPath || cfg.updateMode != "notify" {
		t.Errorf("-p prod resolved to %+v", cfg)
	}
	cfg = resolveConfig(options{profile: "prod", port: "2400"})
	if cfg.port != "2400" {
		t.Errorf("--port did not override the profile's: %+v", cfg)
	}
	if b, _ := os.ReadFile(profilePath("prod")); !strings.Contains(string(b), "port = 2300\n") {
		t.Errorf("--port rewrote the profile:\n%s", b)
	}
	// A profile with no port line gets the default.
	writeTestProfile(t, "bare", "host = bare.example\n")
	if cfg := resolveConfig(options{profile: "bare"}); cfg.port != defaultPort {
		t.Errorf("a profile without a port resolved to %+v", cfg)
	}
}

// One profile and no -p: it is the one, said once, no question asked.
func TestResolveConfigPicksTheOnlyProfileByItself(t *testing.T) {
	sandboxHome(t)
	writeTestProfile(t, "only", "host = only.example\nport = 2222\n")
	var cfg config
	if msg := noTerminalRefusal(t, func() { cfg = resolveConfig(options{}) }); msg != "" {
		t.Fatalf("a single profile asked for a terminal: %s", msg)
	}
	if cfg.host != "only.example" {
		t.Errorf("resolved to %+v, want the only profile", cfg)
	}
}

// Several profiles, no -p, no terminal: a clear refusal that says what to
// type, not a prompt nobody can answer and not a guess.
func TestResolveConfigRefusesToGuessAmongSeveralProfilesWithoutATerminal(t *testing.T) {
	sandboxHome(t)
	writeTestProfile(t, "dev", "host = dev.example\n")
	writeTestProfile(t, "prod", "host = prod.example\n")
	msg := noTerminalRefusal(t, func() { resolveConfig(options{}) })
	if msg == "" {
		t.Fatal("two profiles and no -p resolved without a terminal: which one?")
	}
	for _, want := range []string{"no terminal available", "-p <name>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q: %s", want, msg)
		}
	}
}

// -p on a profile that does not exist offers the wizard; without a terminal
// the refusal points at --host, the non-interactive way.
func TestResolveConfigOffersTheWizardForAnAbsentProfile(t *testing.T) {
	sandboxHome(t)
	msg := noTerminalRefusal(t, func() { resolveConfig(options{profile: "new"}) })
	if msg == "" {
		t.Fatal("an absent profile resolved to something without a terminal")
	}
	if !strings.Contains(msg, "--host") {
		t.Errorf("the refusal does not point at --host: %s", msg)
	}
	// Same for the very first run, with no profile at all.
	msg = noTerminalRefusal(t, func() { resolveConfig(options{}) })
	if msg == "" || !strings.Contains(msg, "--host") {
		t.Errorf("no profile at all, no terminal: %q, want the --host hint", msg)
	}
}
