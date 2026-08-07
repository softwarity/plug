package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Remedies are read by someone who already has a problem, and they are the most
// trusted line plug prints — I followed one of my own for a whole evening while
// it sent a user round in circles, because the docs said one thing and the
// remedy said another and nothing compared them.
//
// These tests read the SOURCE rather than the compiled package, so a machine of
// any OS sees the remedies of all three. That matters: the wrong advice that
// cost that evening lived in doctor_darwin.go and doctor_windows.go, invisible
// from a Linux test run.

var remedyRe = regexp.MustCompile(`remedy: *(?:"((?:[^"\\]|\\.)*)"|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)

// allRemedies collects every remedy string in the doctor sources.
func allRemedies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("doctor*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no doctor sources found: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range remedyRe.FindAllStringSubmatch(string(b), -1) {
			r := m[1]
			if r == "" {
				r = m[2]
			}
			if strings.TrimSpace(r) != "" {
				out[r] = f
			}
		}
	}
	if len(out) < 5 {
		t.Fatalf("only %d remedies found — the extraction is broken, not the code", len(out))
	}
	return out
}

// THE rule this whole file exists for: a remedy tells someone what to DO, so it
// must name a command that exists. `plug down` was handed out for a problem it
// could not solve; a verb that does not exist at all would be worse.
func TestEveryRemedyNamesARealCommand(t *testing.T) {
	verbs := map[string]bool{}
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	for _, m := range regexp.MustCompile(`case "([a-z-]+)":`).FindAllStringSubmatch(string(b), -1) {
		verbs[m[1]] = true
	}
	if len(verbs) < 5 {
		t.Fatalf("only %d verbs parsed from main.go — the extraction is broken", len(verbs))
	}
	// "plug" must start a word (not softwarity/plug) and be followed by a VERB,
	// not by a flag — `plug -p <name>` names an option, and matching it would
	// report a defect that is not there. A test that cries wolf gets ignored,
	// and then it protects nothing.
	call := regexp.MustCompile("(^|[\\s`\"(])plug ([a-z][a-z-]*)")
	for remedy, file := range allRemedies(t) {
		for _, m := range call.FindAllStringSubmatch(remedy, -1) {
			if v := m[2]; !verbs[v] {
				t.Errorf("%s: remedy names `plug %s`, which is not a command:\n  %q", file, v, remedy)
			}
		}
	}
}

// The advice that cost an evening: `plug down` as the way to pick up a new
// version. It never was — closing every session lets the daemon stop by itself,
// and `plug down` strands whatever is still running.
//
// It is advised NOWHERE now, not even for the one state where stopping is the
// answer: a wedged datapath is handled by `plug doctor --fix`, which stops it
// for you. The command lives on the daemon line as a fact ("running (pid …,
// plug down stops it)"), which is a statement, not an instruction.
func TestNoRemedyEverAdvisesPlugDown(t *testing.T) {
	var offenders []string
	for remedy, file := range allRemedies(t) {
		if !strings.Contains(remedy, "plug down") {
			continue
		}
		offenders = append(offenders, file+": "+remedy)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("`plug down` is advised in %d remedy(ies); it belongs on the daemon line as a FACT, "+
			"not as a cure — closing the sessions is what picks up a new version:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// A remedy that only names a state is not a remedy. Each one must contain
// something to run or somewhere to go — this catches "your resolver is stale"
// with no next step, which is the shape doctor's worst messages had.
func TestEveryRemedyTellsYouWhatToDo(t *testing.T) {
	for remedy, file := range allRemedies(t) {
		actionable := false
		for _, verb := range []string{
			"plug ", "re-run", "sudo ", "rm ", "Settings", "install",
			"mount ", "apply ", "redeploy", "add ", "close ", "pin ", "scale ", "attach ",
		} {
			if strings.Contains(remedy, verb) {
				actionable = true
				break
			}
		}
		if !actionable {
			t.Errorf("%s: remedy has no action in it:\n  %q", file, remedy)
		}
	}
}
