package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// isolateHome points plugDir() at a scratch dir: these tests write state, and
// must never touch the real ~/.plug of whoever runs them.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // windows
	return dir
}

// none must be a real off switch: no registry lookup, ever, whatever the state
// file says.
func TestNoneNeverChecks(t *testing.T) {
	old := updateState{checked: time.Now().Add(-30 * 24 * time.Hour)}
	if shouldCheck(updateNone, old, time.Now()) {
		t.Error("update=none asked the registry anyway")
	}
}

// Someone who runs plug fifty times before lunch must not hit a registry fifty
// times — that is what makes the check acceptable in the first place.
func TestCheckIsRateLimitedToOncePerDay(t *testing.T) {
	now := time.Now()
	cases := []struct {
		what string
		last time.Time
		want bool
	}{
		{"just checked", now.Add(-time.Minute), false},
		{"checked this morning", now.Add(-6 * time.Hour), false},
		{"a day and a bit ago", now.Add(-25 * time.Hour), true},
		{"never checked (zero time)", time.Time{}, true},
	}
	for _, c := range cases {
		for _, mode := range []string{updateNotify, updateAuto} {
			if got := shouldCheck(mode, updateState{checked: c.last}, now); got != c.want {
				t.Errorf("%s in mode %s: shouldCheck = %v, want %v", c.what, mode, got, c.want)
			}
		}
	}
}

// An unset or hand-mangled policy must land on notify. none would silently leave
// a cluster behind; auto would roll a SHARED agent nobody asked to have rolled.
func TestUnsetOrGarbagePolicyIsNotify(t *testing.T) {
	for _, in := range []string{"", "yes", "AUTO", "true", "  "} {
		if got := normalizeUpdateMode(in); got != updateNotify {
			t.Errorf("normalizeUpdateMode(%q) = %q, want %q", in, got, updateNotify)
		}
	}
	for _, in := range updateModes {
		if got := normalizeUpdateMode(in); got != in {
			t.Errorf("normalizeUpdateMode(%q) = %q — a valid mode must survive", in, got)
		}
	}
}

func TestUpdateStateRoundTrips(t *testing.T) {
	isolateHome(t)
	cfg := config{host: "h", port: "2222"}
	want := updateState{checked: time.Now().Truncate(time.Second), available: "2.8.0", image: "softwarity/plug:2.7.3"}
	saveUpdateState(cfg, want)

	got := loadUpdateState(cfg)
	if !got.checked.Equal(want.checked) {
		t.Errorf("checked = %v, want %v", got.checked, want.checked)
	}
	if got.available != want.available || got.image != want.image {
		t.Errorf("got (%q,%q), want (%q,%q)", got.available, got.image, want.available, want.image)
	}
}

// The policy is per cluster, so the answer has to be too. One state file for the
// machine would let the local cluster's "2.8.0 is out" be announced for the
// shared one, which is exactly the confusion this scoping exists to avoid.
func TestEachClusterKeepsItsOwnAnswer(t *testing.T) {
	isolateHome(t)
	local := config{host: "localhost", port: "2222"}
	shared := config{host: "cluster.corp", port: "2222"}

	saveUpdateState(local, updateState{checked: time.Now(), available: "2.8.0"})
	saveUpdateState(shared, updateState{checked: time.Now()})

	if got := loadUpdateState(local).available; got != "2.8.0" {
		t.Errorf("the local cluster lost its answer: available = %q", got)
	}
	if got := loadUpdateState(shared).available; got != "" {
		t.Errorf("the shared cluster inherited another cluster's answer: %q", got)
	}
	if updateStatePath(local) == updateStatePath(shared) {
		t.Error("two clusters share one state file")
	}
}

// A cleared state is how "already applied / nothing to say" is recorded, and it
// has to survive the round trip as empty rather than reappear as an update.
func TestAClearedStateStaysCleared(t *testing.T) {
	isolateHome(t)
	cfg := config{host: "h", port: "2222"}
	saveUpdateState(cfg, updateState{checked: time.Now(), available: "2.8.0"})
	saveUpdateState(cfg, updateState{checked: time.Now()})

	if got := loadUpdateState(cfg); got.available != "" {
		t.Errorf("available = %q after clearing, want empty", got.available)
	}
}

// A missing file must not read as "checked just now", or the first check would
// never run on a fresh machine.
func TestAbsentStateReadsAsNeverChecked(t *testing.T) {
	isolateHome(t)
	cfg := config{host: "h", port: "2222"}
	if _, err := os.Stat(updateStatePath(cfg)); !os.IsNotExist(err) {
		t.Fatalf("expected no state file yet, got %v", err)
	}
	if !shouldCheck(updateNotify, loadUpdateState(cfg), time.Now()) {
		t.Error("a machine that has never checked did not check")
	}
}

// Writing a setting must not cost the profile anything else it holds — comments
// included, and keys this version has never heard of.
func TestSettingAPolicyPreservesTheRestOfTheProfile(t *testing.T) {
	isolateHome(t)
	body := "# my cluster\nhost = example.test\nport = 2223\nsomething-newer = keep-me\n"
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath("neo"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	setProfileKey("neo", "update", updateAuto)

	cfg := loadProfile("neo")
	if cfg.host != "example.test" || cfg.port != "2223" {
		t.Errorf("host/port lost: %q %q", cfg.host, cfg.port)
	}
	if cfg.updateMode != updateAuto {
		t.Errorf("updateMode = %q, want %q", cfg.updateMode, updateAuto)
	}
	raw, err := os.ReadFile(profilePath("neo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"# my cluster", "something-newer = keep-me"} {
		if !strings.Contains(string(raw), keep) {
			t.Errorf("profile lost %q:\n%s", keep, raw)
		}
	}

	// And setting it again must replace, not append a second line.
	setProfileKey("neo", "update", updateNone)
	raw, _ = os.ReadFile(profilePath("neo"))
	if n := strings.Count(string(raw), "update ="); n != 1 {
		t.Errorf("update appears %d times after a second set:\n%s", n, raw)
	}
	if loadProfile("neo").updateMode != updateNone {
		t.Error("the second set did not take")
	}
}
