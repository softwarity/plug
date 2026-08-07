package main

import (
	"strings"
	"testing"
	"time"
)

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
