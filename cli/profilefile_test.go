package main

// The profile FILE: what writing host and port leaves of the rest of it, and
// whether the two readers of it (the launcher's and doctor's) read the same
// thing. Both were separate code paths once, and each diverged from the other
// in a way nobody saw until a user did.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// `plug -p prod -H newhost` and `plug init` write host and port. They used to
// write the whole file, so the personal key `plug keygen` had put there and the
// update policy `plug config` had set were gone, silently; the next session
// offered the built-in key and the gateway refused someone it had enrolled.
func TestWriteProfileKeepsTheKeyAndTheUpdatePolicy(t *testing.T) {
	sandboxHome(t)
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(plugDir(), "keys", "prod")
	before := "# the gateway cluster\n" +
		"host = old.example\n" +
		"port = 2222\n" +
		"key = " + keyPath + "\n" +
		"update = auto\n" +
		"colour = teal\n" // a key this version does not know about
	if err := os.WriteFile(profilePath("prod"), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	writeProfile("prod", "new.example", "2200")

	after, err := os.ReadFile(profilePath("prod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# the gateway cluster\n",
		"host = new.example\n",
		"port = 2200\n",
		"key = " + keyPath + "\n",
		"update = auto\n",
		"colour = teal\n",
	} {
		if !strings.Contains(string(after), want) {
			t.Errorf("the profile lost %q:\n%s", strings.TrimSpace(want), after)
		}
	}
	if strings.Contains(string(after), "old.example") {
		t.Errorf("the old host is still in the file:\n%s", after)
	}
	cfg := loadProfile("prod")
	if cfg.host != "new.example" || cfg.port != "2200" || cfg.key != keyPath || cfg.updateMode != updateAuto {
		t.Errorf("loaded back as %+v", cfg)
	}
}

// A profile that does not exist yet is created, host and port and nothing else:
// that is the wizard's and `plug -p new -H host`'s whole job.
func TestWriteProfileCreatesAnAbsentProfile(t *testing.T) {
	sandboxHome(t)
	writeProfile("fresh", "h.example", "2222")
	got, err := os.ReadFile(profilePath("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "host = h.example\nport = 2222\n" {
		t.Fatalf("a new profile reads %q", got)
	}
}

// doctor's reader had its own loop, which never learnt `update` and did not
// skip comments: the MCP tool list_profiles reported an empty update policy for
// every cluster, whatever the file said. One parser now, so the launcher and
// the soft reader cannot disagree about a key again.
func TestReadProfileSoftSeesEveryKeyTheLauncherDoes(t *testing.T) {
	sandboxHome(t)
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(plugDir(), "keys", "neo")
	text := "# hand-edited\n" +
		"host = cluster.example\n" +
		"# port = 9999\n" + // commented out, so the default applies
		"update = auto\n" +
		"key = " + keyPath + "\n"
	if err := os.WriteFile(profilePath("neo"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	soft, err := readProfileSoft("neo")
	if err != nil {
		t.Fatal(err)
	}
	if soft.updateMode != updateAuto {
		t.Errorf("readProfileSoft read update=%q, want %q: list_profiles reports this", soft.updateMode, updateAuto)
	}
	if soft.key != keyPath {
		t.Errorf("readProfileSoft read key=%q, want %q", soft.key, keyPath)
	}
	if soft.host != "cluster.example" || soft.port != defaultPort {
		t.Errorf("readProfileSoft read %s:%s; a commented-out port is no port", soft.host, soft.port)
	}
	hard := loadProfile("neo")
	hard.port = defaultPort // the launcher leaves the default to resolveConfig
	if !reflect.DeepEqual(hard, soft) {
		t.Errorf("the two readers disagree:\n launcher %+v\n doctor   %+v", hard, soft)
	}
}

// A line that is neither blank, a comment nor `key = value` is a finding for
// doctor to report, where it used to be skipped by one reader and the other.
func TestAStrayLineInAProfileIsSaidNotRefused(t *testing.T) {
	cfg, notes, err := parseProfile([]byte("host = h\nthis is not a setting\n"))
	if err != nil || cfg.host != "h" {
		t.Fatalf("a stray line must not stop the profile from loading: %+v %v", cfg, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "line 2") {
		t.Fatalf("the stray line is not reported with its number: %q", notes)
	}
	cfg, notes, err = parseProfile([]byte("host = h\nforward = 5432\n"))
	if err != nil || cfg.host != "h" {
		t.Fatalf("a retired key must still parse: %+v %v", cfg, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "forward") {
		t.Errorf("the retired key is not reported for the launcher to mention: %q", notes)
	}
}

// The terminal a question is asked on is /dev/tty on unix and CONIN$ on
// Windows (ttyDevice, per OS). Three prompts opened "/dev/tty" by its literal
// name, so on Windows `plug -p new <cmd>`, `plug init` and the profile picker
// could never ask anything. The source is read rather than the binary so a
// test run on any OS sees it, and so the next prompt someone writes with the
// literal is caught here rather than by a Windows user.
func TestNoPromptNamesTheTerminalDeviceByItsLiteral(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, literal := range []string{`"/dev/tty"`, `"CONIN$"`} {
			n := strings.Count(src, literal)
			if n == 0 {
				continue
			}
			// The one legitimate home of each literal is the ttyDevice constant.
			if n == 1 && strings.Contains(src, "const ttyDevice = "+literal) {
				continue
			}
			t.Errorf("%s names the terminal device %s directly; open it through openTerminal", f, literal)
		}
	}
	if !strings.Contains(readSource(t, "profiles.go"), "func openTTY") ||
		!strings.Contains(readSource(t, "profiles.go"), "os.Open(ttyDevice)") {
		t.Error("openTerminal no longer opens ttyDevice; the per-OS device is the whole point")
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
