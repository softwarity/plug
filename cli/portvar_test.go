package main

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// A named local port (-s web:8080:PORT) is user-facing grammar: the third field
// of a -s and the {PORT} references in the command are one contract, and the
// command line is the only channel — nothing is exported to the environment.
// Lock it down: a regression here is silent (the child binds a port the cluster
// isn't forwarding to, and simply never receives traffic).

func TestParseExposePortVar(t *testing.T) {
	// A named third field parses as a declaration, not a port.
	for _, c := range []struct{ in, name, cport, pvar string }{
		{"web:8080:PORT", "web", "8080", "PORT"},
		{"web:8080:PROXY_PORT", "web", "8080", "PROXY_PORT"},
		{"my-app:80:p", "my-app", "80", "p"},
		{"a:1:_x9", "a", "1", "_x9"},
	} {
		spec, err := parseExpose(c.in)
		if err != nil {
			t.Fatalf("parseExpose(%q): %v", c.in, err)
		}
		if spec.Name != c.name || spec.ClusterPort != c.cport || spec.PortVar != c.pvar {
			t.Fatalf("parseExpose(%q) = %+v", c.in, spec)
		}
		// Unresolved until resolvePortVars runs — a spec must never be armed
		// with an empty LocalPort silently filled in as ":0" downstream.
		if spec.LocalPort != "" {
			t.Fatalf("parseExpose(%q): LocalPort = %q, want empty until resolved", c.in, spec.LocalPort)
		}
	}

	// A pinned port still parses as a port, with no variable attached.
	spec, err := parseExpose("web:8080:3000")
	if err != nil || spec.LocalPort != "3000" || spec.PortVar != "" {
		t.Fatalf("pinned port: spec = %+v, err = %v", spec, err)
	}

	// The CLUSTER port is agreed in advance — naming it is meaningless, and
	// accepting it would make the two fields look interchangeable.
	if _, err := parseExpose("web:PORT:3000"); err == nil {
		t.Fatal("a named cluster port must be rejected")
	}

	for _, bad := range []string{
		"web:8080:9PORT",  // not a valid env var name (leading digit)
		"web:8080:MY-VAR", // hyphen: valid nowhere as a shell variable
		"web:8080:{PORT}", // the REFERENCE form — braces belong in the command
		"web:8080:my var", // space
		"web:8080:0",      // still a number, still out of range
		"web:8080:65536",
		"web:8080:",
	} {
		if _, err := parseExpose(bad); err == nil {
			t.Errorf("parseExpose(%q) should fail", bad)
		}
	}
}

// The error for a bad third field must offer the way out, not just say no —
// this is the field where someone typing a name for the first time lands.
func TestParseExposePortVarErrorMentionsBothForms(t *testing.T) {
	_, err := parseExpose("web:8080:9PORT")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"port", "name", "{PORT}"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
}

func TestResolvePortVars(t *testing.T) {
	specs := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "8080", PortVar: "PORT"}}
	cmd := []string{"npm", "run", "dev", "--", "--port={PORT}"}

	got, args, err := resolvePortVars(specs, cmd)
	if err != nil {
		t.Fatalf("resolvePortVars: %v", err)
	}
	port := got[0].LocalPort
	if !validPort(port) {
		t.Fatalf("LocalPort = %q, want an allocated port", port)
	}
	// The mapping and the command must agree — that IS the feature.
	if want := "--port=" + port; args[4] != want {
		t.Fatalf("args = %v, want the last arg %q", args, want)
	}
	// Everything else is untouched.
	if !reflect.DeepEqual(args[:4], []string{"npm", "run", "dev", "--"}) {
		t.Fatalf("args = %v", args)
	}
	// The command line is the ONLY channel. Exporting the name as well would be
	// a second way to carry one number — and would silently overwrite whatever
	// the application already kept under that name.
	if v, ok := os.LookupEnv("PORT"); ok {
		t.Fatalf("$PORT was exported as %q — nothing must be injected into the child's environment", v)
	}
}

// Declaring a name and never referencing it allocates a port the child is never
// told about: the mapping gets armed and nothing ever answers it. Silent from
// the cluster's side, so it has to fail here.
func TestResolvePortVarsDeclaredButUnused(t *testing.T) {
	specs := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "8080", PortVar: "PROXY_PORT"}}
	_, _, err := resolvePortVars(specs, []string{"polyglot", "--config=./angular.json"})
	if err == nil {
		t.Fatal("a declaration with no {PROXY_PORT} reference must be rejected")
	}
	// The message must carry the fix, not just the complaint.
	for _, want := range []string{"PROXY_PORT", "--port={PROXY_PORT}", "pin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

// Two mappings, two names: two DIFFERENT ports. One process serving two cluster
// names on one local port is a different intent — see the next test.
func TestResolvePortVarsDistinct(t *testing.T) {
	specs := []tunnel.ExposeSpec{
		{Name: "web", ClusterPort: "8080", PortVar: "WEB"},
		{Name: "api", ClusterPort: "8081", PortVar: "API"},
	}
	got, args, err := resolvePortVars(specs, []string{"srv", "--web={WEB}", "--api={API}"})
	if err != nil {
		t.Fatalf("resolvePortVars: %v", err)
	}
	if got[0].LocalPort == got[1].LocalPort {
		t.Fatalf("both mappings got port %s — they must be independent", got[0].LocalPort)
	}
	if args[1] != "--web="+got[0].LocalPort || args[2] != "--api="+got[1].LocalPort {
		t.Fatalf("args = %v, specs = %+v", args, got)
	}
}

// The same name twice is one port: two cluster names pointing at one listener.
func TestResolvePortVarsSharedName(t *testing.T) {
	specs := []tunnel.ExposeSpec{
		{Name: "web", ClusterPort: "80", PortVar: "PORT"},
		{Name: "web-tls", ClusterPort: "443", PortVar: "PORT"},
	}
	got, args, err := resolvePortVars(specs, []string{"srv", "--listen={PORT}"})
	if err != nil {
		t.Fatalf("resolvePortVars: %v", err)
	}
	if got[0].LocalPort != got[1].LocalPort {
		t.Fatalf("shared name got %s and %s — one name is one port", got[0].LocalPort, got[1].LocalPort)
	}
	// One reference satisfies both declarations — they are the same name.
	if args[1] != "--listen="+got[0].LocalPort {
		t.Fatalf("args = %v, specs = %+v", args, got)
	}
}

// A pinned port is passed through as-is, and no allocation happens.
func TestResolvePortVarsPinnedUntouched(t *testing.T) {
	specs := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "8080", LocalPort: "3000"}}
	cmd := []string{"npm", "start"}
	got, args, err := resolvePortVars(specs, cmd)
	if err != nil {
		t.Fatalf("resolvePortVars: %v", err)
	}
	if got[0].LocalPort != "3000" || !reflect.DeepEqual(args, cmd) {
		t.Fatalf("got = %+v, args = %v", got, args)
	}
}

// A typo'd reference must fail at startup. Left alone it reaches the child as a
// literal "{PROT}", which either crashes it or — worse — makes it fall back to
// a default port the cluster mapping doesn't point at.
func TestResolvePortVarsUnknownReference(t *testing.T) {
	specs := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "8080", PortVar: "PORT"}}
	_, _, err := resolvePortVars(specs, []string{"npm", "--port={PROT}"})
	if err == nil {
		t.Fatal("an undeclared {PROT} must be rejected")
	}
	// The message has to name the typo AND what was actually declared —
	// otherwise the fix is a guess.
	for _, want := range []string{"PROT", "{PORT}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

// A session that doesn't use the feature must not have its argv inspected at
// all: `awk '{print}'` is a normal command, not a broken reference. This is why
// resolvePortVars returns early when no -s named a port.
func TestResolvePortVarsIgnoresBracesWhenUnused(t *testing.T) {
	specs := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "8080", LocalPort: "3000"}}
	cmd := []string{"awk", "{print}", "--x={NOPE}"}
	_, args, err := resolvePortVars(specs, cmd)
	if err != nil {
		t.Fatalf("braces in a session with no named port must be left alone: %v", err)
	}
	if !reflect.DeepEqual(args, cmd) {
		t.Fatalf("args = %v, want untouched %v", args, cmd)
	}
}

// A bare name is NOT substituted — only {…} is. Otherwise every argument
// containing the word would be rewritten (--transport=PORTAL).
func TestSubstitutePortVarsOnlyBraced(t *testing.T) {
	args, used, err := substitutePortVars(
		[]string{"srv", "--transport=PORTAL", "PORT", "--p={PORT}"},
		map[string]string{"PORT": "54321"},
	)
	if err != nil {
		t.Fatalf("substitutePortVars: %v", err)
	}
	want := []string{"srv", "--transport=PORTAL", "PORT", "--p=54321"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	// The bare occurrences must not count as references either — otherwise
	// `--transport=PORTAL` alone would satisfy a declaration.
	if !used["PORT"] || len(used) != 1 {
		t.Fatalf("used = %v, want only the braced reference to count", used)
	}
}

// Several references in one argument, and the command word itself, are all fair
// game — substitution is positional-agnostic.
func TestSubstitutePortVarsRepeated(t *testing.T) {
	args, _, err := substitutePortVars(
		[]string{"--url=http://localhost:{PORT}/{PORT}"},
		map[string]string{"PORT": "4242"},
	)
	if err != nil {
		t.Fatalf("substitutePortVars: %v", err)
	}
	if args[0] != "--url=http://localhost:4242/4242" {
		t.Fatalf("args = %v", args)
	}
}

func TestFreeLocalPort(t *testing.T) {
	a, err := freeLocalPort()
	if err != nil {
		t.Fatalf("freeLocalPort: %v", err)
	}
	n, err := strconv.Atoi(a)
	if err != nil || n < 1 || n > 65535 {
		t.Fatalf("freeLocalPort = %q, want a port number", a)
	}
	// Consecutive calls must not hand out the same port — two -s in one command
	// would otherwise collide.
	b, err := freeLocalPort()
	if err != nil {
		t.Fatalf("freeLocalPort: %v", err)
	}
	if a == b {
		t.Fatalf("two calls both returned %s", a)
	}
}

// The wire format across the launcher→core exec: a named port travels RAW, so
// the core (which owns the grammar) is the one that resolves it. If the
// launcher resolved it instead, an old core would receive a number it never
// asked for and the {…} references would already be gone.
func TestStripLeadingExposesKeepsPortVar(t *testing.T) {
	specs, client, _, rest, err := stripLeadingExposes(
		[]string{"-s", "web:8080:PORT", "npm", "run", "dev", "--", "--port={PORT}"})
	if err != nil || client || len(specs) != 1 {
		t.Fatalf("specs = %+v, client = %v, err = %v", specs, client, err)
	}
	if specs[0].PortVar != "PORT" || specs[0].LocalPort != "" {
		t.Fatalf("spec = %+v, want the declaration carried unresolved", specs[0])
	}
	// The references must still be in the command for the core to substitute.
	if !reflect.DeepEqual(rest, []string{"npm", "run", "dev", "--", "--port={PORT}"}) {
		t.Fatalf("rest = %v", rest)
	}
}

// serveRequired runs before connecting; a named port must pass it, or the
// feature would be rejected before the core ever sees it.
func TestServeRequiredAcceptsPortVar(t *testing.T) {
	if err := serveRequired([]string{"web:8080:PORT"}, false, false); err != nil {
		t.Fatalf("a named local port must be accepted: %v", err)
	}
	if err := serveRequired([]string{"web:8080:9PORT"}, false, false); err == nil {
		t.Fatal("an invalid third field must still be rejected")
	}
}
