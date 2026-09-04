package main

import (
	"regexp"
	"strings"
	"testing"
)

// Every example in the help must be a command plug would actually accept.
//
// This exists because one was not. The --dockerrun entry was written as
// `plug -p prod --dockerrun docker run my-image`, which serveRequired refuses:
// a member of a cluster either names itself with -s or declares itself a client
// with -c, and a container is a member like any other. The help taught a command
// that stops with an error, which is worse than no example, and nothing in the
// build had an opinion about it.
//
// The check runs the same function the launcher runs, so it cannot drift from
// the rule: what serveRequired refuses at runtime, this refuses at build time.
func TestEveryExampleInTheHelpIsAValidInvocation(t *testing.T) {
	// A line that runs something, as opposed to a subcommand: it names a command
	// to run, so the -s / -c rule applies to it. Subcommands (ls, doctor, update)
	// never reach serveRequired, which is why they are excluded by name rather
	// than by shape.
	subcommands := map[string]bool{
		"ls": true, "test": true, "doctor": true, "update": true, "rn": true,
		"rm": true, "version": true, "versions": true, "prune": true,
		"uninstall": true, "about": true, "init": true, "down": true,
		"install-service": true, "remove-service": true, "selftest": true,
	}

	// Indented, which is what tells an invocation from the title line: every
	// example in the help sits under a heading, and the first line of the file
	// is a sentence that happens to start with the word plug.
	line := regexp.MustCompile(`(?m)^[ \t]+plug .*$`)
	examples := line.FindAllString(usage(), -1)
	if len(examples) < 5 {
		t.Fatalf("only %d example lines found in the help; the extraction is broken, not the help", len(examples))
	}

	checked := 0
	for _, raw := range examples {
		fields := strings.Fields(raw)[1:] // drop "plug"
		// The one line that documents a CHOICE rather than an invocation. Its
		// alternation is not argv and there is nothing to check in it.
		if strings.ContainsAny(raw, "|…") {
			continue
		}
		i := 0
		for i < len(fields) && strings.HasPrefix(fields[i], "[") {
			i++ // an optional part, written as [-p profile]
		}
		if i >= len(fields) || subcommands[fields[i]] {
			continue
		}
		var exposes []string
		client := false
		for j := i; j < len(fields); j++ {
			switch fields[j] {
			case "-s", "--serve":
				if j+1 < len(fields) {
					// A written-out placeholder stands for a spec, not for one:
					// what is under test here is the STANCE, whether the example
					// says what its member is to the cluster. Spec syntax has its
					// own tests, and asserting it here would only mean the help
					// could no longer show the shape of an argument.
					spec := fields[j+1]
					if strings.Contains(spec, "<") {
						spec = "app:8080:3000"
					}
					exposes = append(exposes, spec)
				}
			case "-c", "--client":
				client = true
			}
		}
		checked++
		if err := serveRequired(exposes, client); err != nil {
			t.Errorf("the help shows a command plug refuses:\n  %s\n  %v", strings.TrimSpace(raw), err)
		}
	}
	if checked == 0 {
		t.Fatal("no runnable example was checked; the extraction skipped everything it was meant to test")
	}
}

// The options may be written in any order; only the command has to come last.
//
// That is a promise the help now makes, and it used to make the opposite one:
// "-s … place after the other options", which was not true of any version this
// test could find. A sentence like that is worse than silent, because a reader
// obeys it and never discovers it was unnecessary.
//
// Order-independence falls out of parseArgs looping until the first non-flag,
// which is exactly the property worth pinning: it is one `default:` away from
// being lost, and nothing else would notice.
func TestTheOptionsMayComeInAnyOrder(t *testing.T) {
	want, wantCmd := parseArgs([]string{
		"-p", "prod", "-H", "h.example", "--port", "2200", "-s", "api:8080:3000", "-c", "--dockerrun",
		"npm", "run", "dev",
	})

	for _, order := range [][]string{
		{"-H", "h.example", "-p", "prod", "--port", "2200", "-c", "-s", "api:8080:3000", "--dockerrun"},
		{"--dockerrun", "-c", "-s", "api:8080:3000", "--port", "2200", "-H", "h.example", "-p", "prod"},
		{"-s", "api:8080:3000", "--dockerrun", "-p", "prod", "-c", "-H", "h.example", "--port", "2200"},
		{"--port", "2200", "--dockerrun", "-H", "h.example", "-s", "api:8080:3000", "-p", "prod", "-c"},
	} {
		got, gotCmd := parseArgs(append(append([]string{}, order...), "npm", "run", "dev"))
		if got.profile != want.profile || got.host != want.host || got.port != want.port ||
			got.client != want.client || got.dockerRun != want.dockerRun ||
			strings.Join(got.exposes, ",") != strings.Join(want.exposes, ",") {
			t.Errorf("%v parsed differently from the reference order:\n got %+v\nwant %+v", order, got, want)
		}
		if strings.Join(gotCmd, " ") != strings.Join(wantCmd, " ") {
			t.Errorf("%v: the command came out as %q, want %q", order, gotCmd, wantCmd)
		}
	}
}

// And the command is where the options stop. Everything after the first thing
// that is not an option belongs to the command, flags included: `npm run dev
// --port=3000` must reach npm with its own --port, not be read as plug's.
func TestTheCommandOwnsEverythingAfterIt(t *testing.T) {
	o, cmd := parseArgs([]string{"-c", "npm", "run", "dev", "--port", "3000", "-p", "not-a-profile"})
	if o.profile != "" {
		t.Errorf("plug read %q as its own profile, out of the command's arguments", o.profile)
	}
	if got := strings.Join(cmd, " "); got != "npm run dev --port 3000 -p not-a-profile" {
		t.Errorf("the command was rewritten: %q", got)
	}
}
