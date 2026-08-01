package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points plugDir() at a scratch dir: these tests write settings, and
// must never touch the real ~/.plug of whoever runs them.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // windows
	return dir
}

// The default has to be notify and nothing else: none would silently leave a
// machine behind, auto would replace a privileged binary nobody asked to have
// replaced. An unreadable or absent file must land on that same default rather
// than on the zero value.
func TestUpdateModeDefaultsToNotify(t *testing.T) {
	isolateHome(t)
	if got := updateMode(); got != updateNotify {
		t.Fatalf("with no config file, updateMode() = %q, want %q", got, updateNotify)
	}
}

func TestUpdateModeRoundTrips(t *testing.T) {
	isolateHome(t)
	for _, want := range updateModes {
		if err := saveSettings(map[string]string{"update": want}); err != nil {
			t.Fatalf("saveSettings(%q): %v", want, err)
		}
		if got := updateMode(); got != want {
			t.Errorf("updateMode() = %q after saving %q", got, want)
		}
	}
}

// A value that is not one of the three is a config file someone edited by hand.
// Guessing what they meant would be worse than falling back to the default the
// rest of the product documents.
func TestGarbageValueFallsBackToTheDefault(t *testing.T) {
	isolateHome(t)
	if err := saveSettings(map[string]string{"update": "yes-please"}); err != nil {
		t.Fatal(err)
	}
	if got := updateMode(); got != updateNotify {
		t.Errorf("updateMode() = %q for an unknown value, want the %q default", got, updateNotify)
	}
}

// The settings file must not be picked up as a profile: profiles are "*.conf"
// and listProfiles walks the same directory. A machine setting showing up as a
// cluster in `plug ls` would be a confusing bug to chase.
func TestSettingsFileIsNotAProfile(t *testing.T) {
	dir := isolateHome(t)
	if err := saveSettings(map[string]string{"update": updateAuto}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".plug", "config")); err != nil {
		t.Fatalf("settings were not written where expected: %v", err)
	}
	for _, p := range listProfiles() {
		if p == "config" {
			t.Fatal("the settings file is being listed as a profile")
		}
	}
}

// Comments and blank lines are what a user's hand-edited file looks like, and
// the writer emits a header comment itself — reading its own output back has to
// work.
func TestSettingsParsingSurvivesAHandEditedFile(t *testing.T) {
	dir := isolateHome(t)
	if err := os.MkdirAll(filepath.Join(dir, ".plug"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# a comment\n\n  update = auto  \n\n# trailing\n"
	if err := os.WriteFile(filepath.Join(dir, ".plug", "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := updateMode(); got != updateAuto {
		t.Errorf("updateMode() = %q, want %q — whitespace and comments must be tolerated", got, updateAuto)
	}
}

func TestSavedFileIsReadableAndKeyed(t *testing.T) {
	isolateHome(t)
	if err := saveSettings(map[string]string{"update": updateNone}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "update=none") {
		t.Errorf("written file does not carry the setting:\n%s", b)
	}
}
