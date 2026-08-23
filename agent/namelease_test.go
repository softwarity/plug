package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// One name, one live session. Before allocated ports the sshd bind enforced
// this by itself; nameTaken is what replaces it, and unlike the signpost check
// it answers even when no signpost exists — the window right after a boot gc,
// where two live sessions used to end up sharing a name and silently taking
// turns being reachable.
func TestNameTaken(t *testing.T) {
	ours := []portPair{{cluster: "3000", agent: "40001"}}
	live := func(ports ...string) func(string) bool {
		return func(p string) bool {
			for _, up := range ports {
				if p == up {
					return true
				}
			}
			return false
		}
	}
	for _, tc := range []struct {
		name string
		held string
		live func(string) bool
		want bool
	}{
		{"no lease at all — the name is free", "", live(), false},
		{"another session, its port still answers", "40002", live("40002"), true},
		{"another session, but its port is dead — a crash left this", "40002", live(), false},
		{"our own lease, same port: a plain re-serve", "40001", live("40001"), false},
		{"our own name after a reconnect: old port dead, new one ours", "40002", live("40001"), false},
	} {
		if got := nameTaken(tc.held, ours, tc.live); got != tc.want {
			t.Errorf("%s: nameTaken(%q) = %v, want %v", tc.name, tc.held, got, tc.want)
		}
	}
}

// A lease must never refuse on the strength of a port nobody is listening on:
// that is what makes the file safe to leave behind on any failure path.
func TestNameTakenIgnoresADeadHolderWhateverThePorts(t *testing.T) {
	dead := func(string) bool { return false }
	for _, held := range []string{"1", "65535", "40002"} {
		if nameTaken(held, []portPair{{cluster: "80", agent: "50000"}}, dead) {
			t.Errorf("a dead holder (%s) must never take the name", held)
		}
	}
}

// A lease written by an older agent is one line — just the port. It has to read
// back as "port, origin unknown" rather than as a mangled port, because the port
// is what decides whether the name is refused at all: getting it wrong would
// either free a name somebody is using or refuse one nobody is.
func TestALeaseFromAnOlderAgentStillReadsItsPort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	old := nameLeaseDir
	nameLeaseDir = filepath.Join(dir, "names")
	defer func() { nameLeaseDir = old }()

	if err := os.MkdirAll(nameLeaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nameLeaseDir, "svc"), []byte("41943\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := leaseHolder("svc"); got != "41943" {
		t.Errorf("leaseHolder = %q, want the port alone", got)
	}
	if got := leaseOrigin("svc"); got != "" {
		t.Errorf("leaseOrigin = %q, want empty — nothing recorded it", got)
	}
	if got := heldBy("svc", "41943"); got != "agent port 41943" {
		t.Errorf("heldBy = %q — it must not invent a source it does not have", got)
	}
}

// With an origin recorded, the port must still parse exactly, and the refusal
// gains the one thing that makes it actionable: where to go and ask.
func TestALeaseCarriesWhereTheSessionCameFrom(t *testing.T) {
	dir := t.TempDir()
	old := nameLeaseDir
	nameLeaseDir = filepath.Join(dir, "names")
	defer func() { nameLeaseDir = old }()

	t.Setenv("SSH_CLIENT", "10.1.2.3 51544 22")
	takeNameLease("svc", "41943")

	if got := leaseHolder("svc"); got != "41943" {
		t.Errorf("leaseHolder = %q, want 41943", got)
	}
	if got := leaseOrigin("svc"); got != "10.1.2.3" {
		t.Errorf("leaseOrigin = %q, want the client address alone (no ports)", got)
	}
	if got := heldBy("svc", "41943"); got != "agent port 41943, from 10.1.2.3" {
		t.Errorf("heldBy = %q", got)
	}
}

// sshd may tell us nothing (a session set up some other way). Recording an empty
// origin must not corrupt the port line.
func TestNoSSHClientLeavesAOneLineLease(t *testing.T) {
	dir := t.TempDir()
	old := nameLeaseDir
	nameLeaseDir = filepath.Join(dir, "names")
	defer func() { nameLeaseDir = old }()

	t.Setenv("SSH_CLIENT", "")
	takeNameLease("svc", "44615")

	if got := leaseHolder("svc"); got != "44615" {
		t.Errorf("leaseHolder = %q, want 44615", got)
	}
	if got := leaseOrigin("svc"); got != "" {
		t.Errorf("leaseOrigin = %q, want empty", got)
	}
}
