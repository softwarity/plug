package main

import (
	"reflect"
	"strings"
	"testing"
)

// fakeExec answers a fixed reply per command, and records what was asked, so
// the negotiation (env-ofz first, env-of on "unknown command") is proven on
// bytes rather than against a cluster.
type fakeExec struct {
	replies map[string]string
	asked   []string
}

func (f *fakeExec) ExecAll(cmd string) (string, error) {
	f.asked = append(f.asked, cmd)
	if r, ok := f.replies[cmd]; ok {
		return r, nil
	}
	verb := strings.Fields(cmd)[0]
	return "error: unknown command \"" + verb + "\"", nil
}

const pemValue = "-----BEGIN CERTIFICATE-----\nMIIDwzCCAqug\nAwIBAgIUby\n-----END CERTIFICATE-----"

// The whole point of the fix: a PEM in a projected variable survives, newlines
// and all, because the NUL form separates records by a byte a value cannot
// contain. The newline form would have cut it at the first line.
func TestReadWorkloadEnvKeepsAMultilineValueOverNUL(t *testing.T) {
	f := &fakeExec{replies: map[string]string{
		"env-ofz svc": strings.Join([]string{"# a note", "PGPASSWORD=s3cret", "POSTGRES_SSL_CA=" + pemValue}, "\x00"),
	}}
	r, err := readWorkloadEnv(f, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if f.asked[0] != "env-ofz svc" {
		t.Fatalf("must ask env-ofz first, asked %v", f.asked)
	}
	if !reflect.DeepEqual(r.notes, []string{"a note"}) {
		t.Fatalf("notes = %v", r.notes)
	}
	set, _, _ := mergeWorkloadEnvWithEmpty(r.vars, nil, envPolicy{})
	if set["POSTGRES_SSL_CA"] != pemValue {
		t.Fatalf("PEM not kept whole:\n%q\nwant\n%q", set["POSTGRES_SSL_CA"], pemValue)
	}
	if set["PGPASSWORD"] != "s3cret" {
		t.Fatalf("PGPASSWORD = %q", set["PGPASSWORD"])
	}
}

// An agent that does not know env-ofz answers "unknown command"; the client
// falls back to env-of, and the (newline-limited) reply still parses.
func TestReadWorkloadEnvFallsBackToNewline(t *testing.T) {
	f := &fakeExec{replies: map[string]string{
		"env-of svc": "# note\nPGPASSWORD=s3cret",
		// env-ofz svc is absent -> fakeExec answers "unknown command"
	}}
	r, err := readWorkloadEnv(f, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 2 || f.asked[0] != "env-ofz svc" || f.asked[1] != "env-of svc" {
		t.Fatalf("must try env-ofz then fall back to env-of, asked %v", f.asked)
	}
	set, _, _ := mergeWorkloadEnvWithEmpty(r.vars, nil, envPolicy{})
	if set["PGPASSWORD"] != "s3cret" || !reflect.DeepEqual(r.notes, []string{"note"}) {
		t.Fatalf("fallback parse: vars=%v notes=%v", set, r.notes)
	}
}

// The agent could answer but refused (a right it lacks): surfaced as agentErr,
// not as a variable, and not mistaken for the unknown-verb fallback.
func TestReadWorkloadEnvSurfacesAgentError(t *testing.T) {
	f := &fakeExec{replies: map[string]string{
		"env-ofz svc": "error: the agent may not exec into pods in ns",
	}}
	r, err := readWorkloadEnv(f, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if r.agentErr == "" || len(r.vars) != 0 {
		t.Fatalf("expected an agent error, got vars=%v err=%q", r.vars, r.agentErr)
	}
	if len(f.asked) != 1 {
		t.Fatalf("a real error is not the unknown-verb fallback; must not retry: %v", f.asked)
	}
}
