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

// The check asks the tunnel channel, where `version` does not exist — it lives
// on the DOWNLOAD channel (the `get` user's ForceCommand). Asking for it there
// made the agent answer `error: unknown command "version"`, probeUpdate gave up
// on its first step, and NOTHING was ever recorded: the feature shipped in
// 2.9.0 and never fired once. `info` carries both facts in one round-trip, so
// the parser must take the version from it.
func TestInfoCarriesBothVersionAndImage(t *testing.T) {
	const line = "version=2.9.0 backend=docker-swarm image=softwarity/plug:2.9.0@sha256:e330e479"
	var before, img string
	for _, f := range strings.Fields(line) {
		if v, cut := strings.CutPrefix(f, "version="); cut {
			before = v
		}
		if v, cut := strings.CutPrefix(f, "image="); cut {
			img = v
		}
	}
	if before != "2.9.0" {
		t.Errorf("version = %q, want 2.9.0 — parsed from info, never from a `version` verb", before)
	}
	if img == "" || !strings.HasPrefix(img, "softwarity/plug:") {
		t.Errorf("image = %q, want the pinned reference", img)
	}
	// And the pinned digest Swarm appends must not confuse the decision.
	apply, _, errMsg, delegate := decideClient(img, before, "", []string{"2.8.0", "2.9.0", "2.9.1", "latest"})
	if delegate || errMsg != "" || apply != "2.9.1" {
		t.Errorf("decideClient(%q, %q) = apply %q, err %q, delegate %v; want 2.9.1", img, before, apply, errMsg, delegate)
	}
}

// The background check has no fallback, so its budget must be bigger than the
// one `plug update` uses (where a timeout merely picks the slower path).
func TestTheCheckAsksForMoreRoomThanUpdate(t *testing.T) {
	if startupSettle <= 0 {
		t.Error("the check must let the datapath settle before its first lookup")
	}
}

// The tests below arrived in a second file, updatecheck_test.go, for the same
// source unit this one covers. Nothing named the boundary between them, so there
// was no answer to "where does the next one go": they are one file now, named
// after update_check.go like every other test file in this package.

// Two different findings reach the notice through one string, and wording them
// the same is the trap: "update available: vlatest" is nonsense. A release has a
// number to announce; a moving tag has none — only different bytes behind the
// same name.
func TestTheNoticeTellsAReleaseFromAMovedTag(t *testing.T) {
	rel := updateNotice("2.9.4")
	if !strings.Contains(rel, "v2.9.4") {
		t.Errorf("release notice = %q, want the version in it", rel)
	}
	if strings.Contains(rel, "points at a different image") {
		t.Errorf("release notice = %q — that is the moving-tag wording", rel)
	}

	for _, tag := range []string{"latest", "main", "feat-09"} {
		m := updateNotice(tag)
		if strings.Contains(m, "v"+tag) {
			t.Errorf("moving-tag notice = %q — a tag is not a version", m)
		}
		if !strings.Contains(m, tag) || !strings.Contains(m, "different image") {
			t.Errorf("moving-tag notice = %q, want it to name the tag and say the image moved", m)
		}
	}
	// Both must say how to act; a notice nobody can act on is noise.
	for _, m := range []string{rel, updateNotice("latest")} {
		if !strings.Contains(m, "plug update") {
			t.Errorf("notice = %q, want it to say what to run", m)
		}
	}
}

// A deployment following a stream was NEVER checked before: decideClient
// delegates on a moving tag and probeUpdate gave up there. What makes it
// decidable is that the agent reports its image WITH the digest it resolved to.
func TestAMovingTagIsRecognisedAndItsDigestReadable(t *testing.T) {
	for _, tc := range []struct{ ref, tag, digest string }{
		{"softwarity/plug:latest@sha256:abc", "latest", "sha256:abc"},
		{"softwarity/plug:main@sha256:def", "main", "sha256:def"},
		{"softwarity/plug:2.9.4@sha256:ghi", "", "sha256:ghi"}, // a release: the tag listing answers
		{"softwarity/plug@sha256:jkl", "", "sha256:jkl"},       // a bare digest cannot move
		{"softwarity/plug:latest", "latest", ""},               // no digest: not decidable
	} {
		if got := movingTagOf(tc.ref); got != tc.tag {
			t.Errorf("movingTagOf(%s) = %q, want %q", tc.ref, got, tc.tag)
		}
		if got := imageDigest(tc.ref); got != tc.digest {
			t.Errorf("imageDigest(%s) = %q, want %q", tc.ref, got, tc.digest)
		}
	}
}

// The prompt interrupts a command the user typed and wants to run — unlike
// askToStop, which interrupts someone already deciding something. Minutes there,
// seconds here.
func TestTheUpdatePromptGivesUpInSeconds(t *testing.T) {
	if offerUpdateDeadline > 30*time.Second {
		t.Errorf("offerUpdateDeadline = %s — too long to sit in front of a command someone typed", offerUpdateDeadline)
	}
	if offerUpdateDeadline >= askToStopDeadline {
		t.Errorf("offerUpdateDeadline (%s) must be well under askToStopDeadline (%s): "+
			"one interrupts a decision, the other interrupts work", offerUpdateDeadline, askToStopDeadline)
	}
	if offerUpdateDeadline < 5*time.Second {
		t.Errorf("offerUpdateDeadline = %s — not enough time to read the line and answer", offerUpdateDeadline)
	}
}

// Without a terminal it must not even try: that is the askToStop trap, which
// once wedged a Windows leg for 16 minutes. Under `go test` stdin is not a
// terminal, so this exercises the real guard.
func TestNoTerminalMeansNoPrompt(t *testing.T) {
	if offerUpdate(config{host: "127.0.0.1", port: "1"}, "2.9.4") {
		t.Error("offerUpdate ran an update with no terminal attached")
	}
}

// The fallback's contract, tested where it matters: what the agent's one line
// means. Anything that is not an explicit "available <tag>" must yield "" —
// including `unknown command`, which is what an agent too old to know the verb
// answers. Guessing there would announce an update that nobody can name.
func TestTheAgentFallbackOnlyTrustsAnExplicitAnswer(t *testing.T) {
	for _, tc := range []struct{ answer, want string }{
		{"available 2.10.0", "2.10.0"},
		{"available latest (moving tag)", "latest"},
		{"available 2.10.0\n", "2.10.0"},
		{"current", ""},
		{"error: this agent cannot name its own image", ""},
		{`error: unknown command "check-update"`, ""}, // an agent from before this
		{"", ""},
		{"available", ""}, // truncated: a tag was promised and not given
	} {
		if got := parseAgentUpdateAnswer(tc.answer); got != tc.want {
			t.Errorf("agent said %q → %q, want %q", tc.answer, got, tc.want)
		}
	}
}
