//go:build !windows

package main

import "testing"

func TestResolveDropTarget(t *testing.T) {
	tests := []struct {
		name             string
		euid, ruid, rgid int
		sudoUID, sudoGID string
		wantUID, wantGID int
		wantOK           bool
	}{
		{
			name: "linux caps: unprivileged, nothing to drop",
			euid: 501, ruid: 501, rgid: 20,
			wantOK: false,
		},
		{
			name: "macOS setuid-root: drop to the real user",
			euid: 0, ruid: 501, rgid: 20,
			wantUID: 501, wantGID: 20, wantOK: true,
		},
		{
			name: "sudo plug: drop to the invoker sudo recorded",
			euid: 0, ruid: 0, rgid: 0, sudoUID: "501", sudoGID: "20",
			wantUID: 501, wantGID: 20, wantOK: true,
		},
		{
			name: "genuine root, no sudo: don't guess a user",
			euid: 0, ruid: 0, rgid: 0,
			wantOK: false,
		},
		{
			name: "root euid, root ruid, blank sudo env: stay root",
			euid: 0, ruid: 0, rgid: 0, sudoUID: "", sudoGID: "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, gid, ok := resolveDropTarget(tt.euid, tt.ruid, tt.rgid, tt.sudoUID, tt.sudoGID)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if uid != tt.wantUID || gid != tt.wantGID {
				t.Fatalf("got uid=%d gid=%d, want uid=%d gid=%d", uid, gid, tt.wantUID, tt.wantGID)
			}
		})
	}
}

func TestCapGroups(t *testing.T) {
	many := make([]uint32, 30)
	for i := range many {
		many[i] = uint32(i)
	}
	// macOS: setgroups(2) rejects >16, so the child's exec would fail with EINVAL.
	if got := capGroups(many, "darwin"); len(got) != 16 {
		t.Errorf("darwin: want 16 groups, got %d", len(got))
	}
	// Linux allows far more — leave the list intact.
	if got := capGroups(many, "linux"); len(got) != 30 {
		t.Errorf("linux: want 30 groups, got %d", len(got))
	}
	// Under the cap: unchanged on every OS.
	few := []uint32{20, 12, 61, 79}
	if got := capGroups(few, "darwin"); len(got) != 4 {
		t.Errorf("darwin under cap: want 4 groups, got %d", len(got))
	}
}
