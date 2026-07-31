package main

import "testing"

// --force crosses the launcher→core exec as leading argv, like -s and -c, so
// the core has to strip it back off.
func TestStripLeadingExposesTakesForceOff(t *testing.T) {
	specs, client, force, rest, err := stripLeadingExposes(
		[]string{"--force", "-s", "web:8080:3000", "npm", "run", "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !force {
		t.Error("--force was not recognised")
	}
	if client {
		t.Error("client set by a command line that has no -c")
	}
	if len(specs) != 1 || specs[0].Name != "web" {
		t.Errorf("specs = %+v, want the one web mapping", specs)
	}
	if len(rest) != 3 || rest[0] != "npm" {
		t.Errorf("command = %q, want it to start at npm", rest)
	}

	// Absent unless asked for — no session forces by accident.
	if _, _, force, _, err = stripLeadingExposes([]string{"-s", "web:8080:3000", "npm"}); err != nil || force {
		t.Errorf("force = %v (err %v), want false on a plain command line", force, err)
	}
}

// --force takes a NAME from its holder, so it means nothing without -s. Silently
// ignoring it would leave you believing you had forced something.
func TestForceWithoutServeIsRefused(t *testing.T) {
	if err := serveRequired(nil, true, true); err == nil {
		t.Error("--force with -c was accepted")
	}
	if err := serveRequired(nil, false, true); err == nil {
		t.Error("--force with neither -s nor -c was accepted")
	}
	if err := serveRequired([]string{"web:8080:3000"}, false, true); err != nil {
		t.Errorf("--force alongside -s was refused: %v", err)
	}
}
