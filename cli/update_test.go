package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateWord(t *testing.T) {
	cases := map[string]string{
		"updating service plug — rolling":                    "updating",
		"current v2.2.0 — image softwarity/plug:2 unchanged": "current",
		"error: unknown command \"self-update\"":             "error:",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := updateWord(in); got != want {
			t.Errorf("updateWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMaxCachedRelease pins the stale-launcher hint's source of truth: newest
// RELEASED core in the cache, numeric compare, dev builds ignored.
func TestMaxCachedRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	// The store is per-OS now — on macOS it sits outside $HOME, because the core
	// is run as root there. Point it at the temp dir rather than rebuilding a
	// path that is no longer the same on every platform.
	store := filepath.Join(home, "store")
	savedStore := versionsDir
	versionsDir = func() string { return store }
	defer func() { versionsDir = savedStore }()

	if got := maxCachedRelease(); got != "" {
		t.Fatalf("empty cache: got %q", got)
	}
	for _, d := range []string{"dev+abc", "2.2.0+x", "2.10.0+y", "not-a-version"} {
		if err := os.MkdirAll(filepath.Join(store, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := maxCachedRelease(); got != "2.10.0+y" {
		t.Fatalf("got %q, want 2.10.0+y (numeric compare, dev ignored)", got)
	}
}

// TestReplaceBinary exercises the swap on every OS (Windows included in CI):
// the target must hold the new bytes afterwards, with no .old left behind on a
// non-running file.
func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "plug-bin")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("new-binary-content"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new-binary-content" {
		t.Fatalf("target holds %q (%v), want the new content", got, err)
	}
	if _, err := os.Stat(target + ".old"); err == nil {
		t.Error("a .old copy survived on an unlocked file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover temp files in %s: %d entries", dir, len(entries))
	}
}

// The agent's reply is a protocol: its FIRST WORD is the verdict cmdUpdate
// switches on. The agent and the CLI are separate binaries on separate release
// trains, so pin the exact sentences the agent produces — a reworded reply that
// no longer starts with a known verdict falls through to the "unknown command"
// branch and reports an upgrade that did happen as a failure.
func TestUpdateWordOnAgentReplies(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		// Swarm / k8s: a retarget or a re-resolve was triggered.
		{"updating service shop_plug - moving the deployment from softwarity/plug:2.3.0 to softwarity/plug:2.4.0, and the task rolls", "updating"},
		{"updating deployment plug (namespace plug) — re-resolving the moving tag softwarity/plug:latest", "updating"},
		// Already newest: answered without rolling anything.
		{"current v2.4.0 — already the newest release published for softwarity/plug:2.4.0", "current"},
		{"current v2.4.0 — image softwarity/plug:latest unchanged", "current"},
		// Compose: the image is local, the recreate is the caller's move.
		{"pulled softwarity/plug:2.4.0 — moving the deployment from softwarity/plug:2.3.0 to softwarity/plug:2.4.0; the agent cannot recreate its own container: set the plug service's image to softwarity/plug:2.4.0 in your compose file, then: docker compose up -d plug", "pulled"},
		{"error: the agent's node is not a swarm manager - from one, run: docker service update --image softwarity/plug:2.4.0 shop_plug", "error:"},
	} {
		if got := updateWord(c.reply); got != c.want {
			t.Errorf("updateWord(%q) = %q, want %q", c.reply, got, c.want)
		}
	}
}

// A rollout that lands on the build already running ends the wait as soon as
// the new instance answers, not 90s later with a warning that it is stuck. The
// instance is told by its host key, which an agent makes each time it starts.
func TestARolloutOnTheSameBuildEndsWhenTheNewAgentAnswers(t *testing.T) {
	saved, savedPoll := agentVersionAndKey, agentRollPoll
	defer func() { agentVersionAndKey, agentRollPoll = saved, savedPoll }()
	agentRollPoll = time.Millisecond

	answers := []string{"old", "", "old", "new"} // the old pod, mid-roll, the old pod again, the new one
	calls := 0
	agentVersionAndKey = func(config) (string, string, error) {
		k := answers[min(calls, len(answers)-1)]
		calls++
		if k == "" {
			return "", "", errors.New("connection refused")
		}
		return "2.25.1", "SHA256:" + k, nil
	}
	after, restarted := waitNewVersion(config{}, "2.25.1", "SHA256:old")
	if after != "2.25.1" || !restarted || calls != 4 {
		t.Errorf("got %q restarted=%v after %d polls; want the same version, restarted, at the 4th poll", after, restarted, calls)
	}

	// A new version still wins at once, and an unknown first key never ends it early.
	calls = 0
	answers = []string{"old", "new"}
	agentVersionAndKey = func(config) (string, string, error) {
		calls++
		return "2.26.0", "SHA256:" + answers[min(calls-1, 1)], nil
	}
	if after, restarted := waitNewVersion(config{}, "2.25.1", ""); after != "2.26.0" || restarted {
		t.Errorf("a new version read as %q restarted=%v", after, restarted)
	}
}
