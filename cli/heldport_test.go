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

// The offer to stop a process is only safe when the record provably IS the
// holder. Getting this wrong points "stop it?" at whatever the OS has since
// given that PID to.
func TestHolderIsOurs(t *testing.T) {
	const refusal = `"web" is already exposed by another live session (agent port 40001) — one -s per name at a time`
	for _, tc := range []struct {
		why  string
		rec  *servedRecord
		msg  string
		want bool
	}{
		{"our own session, same port", &servedRecord{pid: 42, port: "40001"}, refusal, true},
		{"someone else's — a different port", &servedRecord{pid: 42, port: "40002"}, refusal, false},
		{"nothing recorded here", nil, refusal, false},
		{"a record from before ports were recorded", &servedRecord{pid: 42, port: ""}, refusal, false},
		{"an agent too old to name the port", &servedRecord{pid: 42, port: "40001"},
			`"web" is already exposed by another live session`, false},
	} {
		if got := holderIsOurs(tc.rec, tc.msg); got != tc.want {
			t.Errorf("%s: holderIsOurs = %v, want %v", tc.why, got, tc.want)
		}
	}
}
