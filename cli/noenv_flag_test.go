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

// And nothing said means everything projected: the zero policy.
func TestNoFlagMeansProjectEverything(t *testing.T) {
	_, _, p, rest, _ := stripLeadingFlags([]string{"-s", "api:8080:PORT", "npm"})
	if p.off || len(p.drop) != 0 || !reflect.DeepEqual(rest, []string{"npm"}) {
		t.Fatalf("policy=%+v rest=%v", p, rest)
	}
}
