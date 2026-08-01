package main

import (
	"os"
	"testing"
	"time"
)

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

func TestUpdateStateRoundTrips(t *testing.T) {
	isolateHome(t)
	want := updateState{checked: time.Now().Truncate(time.Second), available: "2.8.0", image: "softwarity/plug:2.7.3"}
	saveUpdateState(want)

	got := loadUpdateState()
	if !got.checked.Equal(want.checked) {
		t.Errorf("checked = %v, want %v", got.checked, want.checked)
	}
	if got.available != want.available || got.image != want.image {
		t.Errorf("got (%q,%q), want (%q,%q)", got.available, got.image, want.available, want.image)
	}
}

// A cleared state is how "already applied / nothing to say" is recorded, and it
// has to survive the round trip as empty rather than reappear as an update.
func TestAClearedStateStaysCleared(t *testing.T) {
	isolateHome(t)
	saveUpdateState(updateState{checked: time.Now(), available: "2.8.0"})
	saveUpdateState(updateState{checked: time.Now()})

	if got := loadUpdateState(); got.available != "" {
		t.Errorf("available = %q after clearing, want empty", got.available)
	}
}

// A missing file must not read as "checked just now", or the first check would
// never run on a fresh machine.
func TestAbsentStateReadsAsNeverChecked(t *testing.T) {
	isolateHome(t)
	if _, err := os.Stat(updateStatePath()); !os.IsNotExist(err) {
		t.Fatalf("expected no state file yet, got %v", err)
	}
	if !shouldCheck(updateNotify, loadUpdateState(), time.Now()) {
		t.Error("a machine that has never checked did not check")
	}
}
