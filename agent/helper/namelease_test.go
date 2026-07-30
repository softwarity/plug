package main

import "testing"

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
