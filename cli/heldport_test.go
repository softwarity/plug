package main

import "testing"

// heldPort is what tells OUR forgotten session from someone else's. Read the
// port wrong and we would offer to kill a process that is not the holder — a
// leftover record naming a PID the OS has since handed to something innocent.
func TestHeldPort(t *testing.T) {
	for _, tc := range []struct {
		why  string
		msg  string
		want string
	}{
		{
			"the agent's refusal, as it is worded",
			`"web" is already exposed by another live session (agent port 40001) — one -s per name at a time`,
			"40001",
		},
		{
			"an agent too old to name the port",
			`"web" is already exposed by another live session — one -s per name at a time`,
			"",
		},
		{"some other refusal entirely", "the agent has no orchestrator access", ""},
		{"the marker with nothing after it", "held on agent port ", ""},
		{"trailing text must not be swallowed", "agent port 40001) — one -s", "40001"},
	} {
		if got := heldPort(tc.msg); got != tc.want {
			t.Errorf("%s: heldPort(%q) = %q, want %q", tc.why, tc.msg, got, tc.want)
		}
	}
}
