package main

import (
	"reflect"
	"testing"
)

// --no-env travels the same way as -s and -c: the launcher puts it at the head
// of the core's argv and the core strips it back. Both spellings, and the
// command that follows must come out untouched - `npm` is not a key list.
func TestNoEnvIsStrippedFromTheCoreArgvInBothSpellings(t *testing.T) {
	_, _, p, rest, err := stripLeadingFlags([]string{"--no-env", "-s", "api:8080:PORT", "npm", "run", "dev"})
	if err != nil || !p.off || !reflect.DeepEqual(rest, []string{"npm", "run", "dev"}) {
		t.Fatalf("bare --no-env: off=%v rest=%v err=%v", p.off, rest, err)
	}
	_, _, p, rest, err = stripLeadingFlags([]string{"-s", "api:8080:PORT", "--no-env", "DB_URL,API_KEY", "npm", "run", "dev"})
	if err != nil || p.off || !p.drop["DB_URL"] || !p.drop["API_KEY"] || !reflect.DeepEqual(rest, []string{"npm", "run", "dev"}) {
		t.Fatalf("--no-env A,B: drop=%v rest=%v err=%v", p.drop, rest, err)
	}
	// A command right after a bare --no-env must not be eaten as a key list.
	_, _, p, rest, _ = stripLeadingFlags([]string{"--no-env", "MAKE", "test"})
	if p.off || !p.drop["MAKE"] || !reflect.DeepEqual(rest, []string{"test"}) {
		t.Fatalf("an upper-case word after --no-env IS a key list by the rule: drop=%v rest=%v", p.drop, rest)
	}
	_, _, p, rest, _ = stripLeadingFlags([]string{"--no-env", "make", "test"})
	if !p.off || !reflect.DeepEqual(rest, []string{"make", "test"}) {
		t.Fatalf("a lower-case command after a bare --no-env must be left alone: off=%v rest=%v", p.off, rest)
	}
}

// --env-of names the workload whose environment the command gets, whatever -s
// does: it travels the same way, in either order with --no-env A,B (the keys
// still left out of it), and a bare --no-env beside it is a contradiction the
// core refuses too, for an argv an older launcher forwarded unexamined.
func TestEnvOfIsStrippedAndKeptBesideTheKeyList(t *testing.T) {
	_, client, p, rest, err := stripLeadingFlags([]string{"-c", "--env-of", "orders-svc", "python", "job.py"})
	if err != nil || !client || p.from != "orders-svc" || p.off || !reflect.DeepEqual(rest, []string{"python", "job.py"}) {
		t.Fatalf("-c --env-of: client=%v from=%q off=%v rest=%v err=%v", client, p.from, p.off, rest, err)
	}
	specs, _, p, rest, err := stripLeadingFlags([]string{"-s", "api:8080:PORT", "--env-of", "orders-svc", "--no-env", "API_KEY", "npm", "run", "dev"})
	if err != nil || len(specs) != 1 || p.from != "orders-svc" || !p.drop["API_KEY"] || !reflect.DeepEqual(rest, []string{"npm", "run", "dev"}) {
		t.Fatalf("-s --env-of --no-env A: from=%q drop=%v rest=%v err=%v", p.from, p.drop, rest, err)
	}
	_, _, p, _, err = stripLeadingFlags([]string{"--no-env", "API_KEY", "--env-of", "orders-svc", "npm"})
	if err != nil || p.from != "orders-svc" || !p.drop["API_KEY"] {
		t.Fatalf("the other order: from=%q drop=%v err=%v", p.from, p.drop, err)
	}
	for _, argv := range [][]string{
		{"--no-env", "--env-of", "orders-svc", "npm"},
		{"--env-of", "orders-svc", "--no-env", "npm"},
	} {
		if _, _, _, _, err := stripLeadingFlags(argv); err == nil {
			t.Fatalf("%v: a bare --no-env beside --env-of must be refused", argv)
		}
	}
	if _, err := envPolicyOf(true, "", "orders-svc"); err == nil {
		t.Fatal("envPolicyOf must refuse a bare --no-env with --env-of")
	}
	if p, err := envPolicyOf(true, "A,B", "orders-svc"); err != nil || p.from != "orders-svc" || !p.drop["A"] {
		t.Fatalf("--no-env A,B with --env-of is fine: %+v %v", p, err)
	}
}

// The launcher reads --env-of and both spellings of --no-env, the glued one
// included: `--no-env=A,B` used to fall through as an unknown flag and reach
// the command.
func TestParseArgsReadsEnvOfAndGluedNoEnv(t *testing.T) {
	o, rest := parseArgs([]string{"-c", "--env-of=orders-svc", "--no-env=A,B", "python", "job.py"})
	if o.envOf != "orders-svc" || !o.noEnv || o.noEnvList != "A,B" || !reflect.DeepEqual(rest, []string{"python", "job.py"}) {
		t.Fatalf("glued: %+v rest=%v", o, rest)
	}
	o, rest = parseArgs([]string{"--env-of", "orders-svc", "-s", "api:8080:PORT", "npm", "run"})
	if o.envOf != "orders-svc" || len(o.exposes) != 1 || !reflect.DeepEqual(rest, []string{"npm", "run"}) {
		t.Fatalf("spaced: %+v rest=%v", o, rest)
	}
}

// And nothing said means everything projected: the zero policy.
func TestNoFlagMeansProjectEverything(t *testing.T) {
	_, _, p, rest, _ := stripLeadingFlags([]string{"-s", "api:8080:PORT", "npm"})
	if p.off || len(p.drop) != 0 || !reflect.DeepEqual(rest, []string{"npm"}) {
		t.Fatalf("policy=%+v rest=%v", p, rest)
	}
}
