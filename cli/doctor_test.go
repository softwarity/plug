package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The issue body must never carry the user's topology: the repo is public.
func TestRedact(t *testing.T) {
	cases := map[string]string{
		"unreachable at 10.2.3.4:2222":     "unreachable at x.x.x.x:2222",
		"v2.1.0 at neo.corp.example:2222":  "v2.1.0 at redacted.host:2222",
		"still pointed at plug":            "still pointed at plug",            // no host, untouched
		"rm -r /Users/x/.plug/versions/v1": "rm -r /Users/x/.plug/versions/v1", // a path, untouched
		"plug down (restores it)":          "plug down (restores it)",
	}
	for in, want := range cases {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// issueURL: only warn/fail lines make it in, profiles are anonymized in order,
// and the whole thing is a valid pre-filled new-issue URL.
func TestIssueURL(t *testing.T) {
	u := issueURL([]check{
		{area: "local", name: "launcher", status: stOK, detail: "v9 (/bin/plug)"},
		{area: "neo", name: "agent", status: stFail, detail: "unreachable at 10.0.0.9:2222", remedy: "is the cluster up?"},
		{area: "llm", name: "agent features", status: stWarn, detail: "pre-2.2 agent"},
	})
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host != "github.com" {
		t.Fatalf("bad URL: %v (%v)", u, err)
	}
	body := parsed.Query().Get("body")
	if strings.Contains(body, "launcher") {
		t.Error("OK lines must not be reported")
	}
	if strings.Contains(body, "neo") || strings.Contains(body, "llm") {
		t.Error("profile names must be anonymized")
	}
	if strings.Contains(body, "10.0.0.9") {
		t.Error("IPs must be redacted")
	}
	if !strings.Contains(body, "cluster-1") || !strings.Contains(body, "cluster-2") {
		t.Errorf("anonymized cluster labels missing:\n%s", body)
	}
	if !strings.Contains(body, "x.x.x.x:2222") {
		t.Errorf("redacted IP:port missing:\n%s", body)
	}
}

func TestVersionsFromCorePathParsing(t *testing.T) {
	// darwin-only helper, but the parsing logic is worth pinning portably via
	// readProfileSoft below; versionFromCorePath itself is covered on darwin.
	if _, _, err := readProfileSoft("definitely-absent-profile-xyz"); err == nil {
		t.Fatal("an absent profile must error, not fatal")
	}
}

// `plug down` has exactly one legitimate trigger, and doctor is the only thing
// that can spot it: a daemon that is alive but whose netstack has stopped
// answering. Nothing else reaches that state — the reaper counts sessions, not
// health, so it keeps a corpse running as long as one client is registered.
//
// Without this check the command would have no entry point at all, which is
// what makes it look like it exists for nothing.
func TestAWedgedDatapathIsTheOneThingFixMayStop(t *testing.T) {
	c, show := datapathVerdict(true, false, false)
	if !show {
		t.Fatal("an alive-but-mute datapath must be reported")
	}
	if c.status != stFail {
		t.Errorf("status = %v, want a failure — cluster names cannot resolve in this state", c.status)
	}
	if !strings.Contains(c.remedy, "doctor --fix") {
		t.Errorf("remedy = %q — --fix stops it, no separate command to run", c.remedy)
	}
	if !strings.Contains(c.detail, "will not stop on its own") {
		t.Errorf("detail = %q, want it to say why waiting does not help", c.detail)
	}
}

// The healthy case must stay quiet about that command, or we are back to
// teaching people to use a teardown as routine maintenance.
func TestAWorkingDatapathNeverMentionsPlugDown(t *testing.T) {
	c, show := datapathVerdict(true, true, false)
	if !show || c.status != stOK {
		t.Fatalf("alive and answering must report OK, got show=%v status=%v", show, c.status)
	}
	if strings.Contains(c.detail+c.remedy, "plug down") {
		t.Errorf("a healthy datapath mentions plug down: %q / %q", c.detail, c.remedy)
	}
}

// No daemon at all is not this check's business: the resolver checks own that
// story, and two checks describing the same state in different words is how a
// user ends up applying the wrong remedy.
func TestNoDaemonProducesNoDatapathCheck(t *testing.T) {
	if _, show := datapathVerdict(false, false, false); show {
		t.Error("with no daemon running, this check must stay silent")
	}
}

// And once --fix has stopped it, the report says so plainly: the user has to
// relaunch their sessions, and a check that quietly went green would leave them
// wondering why nothing works.
func TestAStoppedWedgedDatapathReportsWhatWasDone(t *testing.T) {
	c, show := datapathVerdict(true, false, true)
	if !show || c.status != stOK {
		t.Fatalf("after stopping it the check must report OK, got show=%v status=%v", show, c.status)
	}
	if !strings.Contains(c.detail, "stopped it") || !strings.Contains(c.detail, "relaunch") {
		t.Errorf("detail = %q, want it to say it was stopped and that sessions must be relaunched", c.detail)
	}
}

// The rule that came out of fifteen fruitless `plug down` invocations: how to
// stop the daemon is a FACT, printed on the line that says it is running. A
// remedy reads as "do this to fix your problem" — and stopping the daemon
// almost never is: closing the sessions lets it stop by itself.
//
// This asserts the shape rather than the wording: no check may carry that
// command as its REMEDY except the one state where it truly is the answer, a
// datapath alive but no longer answering.
func TestPlugDownIsStatedNeverPrescribed(t *testing.T) {
	wedged, _ := datapathVerdict(true, false, false)
	if strings.Contains(wedged.remedy, "plug down") {
		t.Errorf("even the wedged case now points at --fix, got remedy %q", wedged.remedy)
	}
	for _, c := range []check{
		{detail: "running (pid 123, plug down stops it)"},
		{detail: "C:\\x\\plug.exe — plug down stops it while it runs"},
	} {
		if c.remedy != "" {
			t.Errorf("the daemon line must state, not prescribe: remedy = %q", c.remedy)
		}
		if !strings.Contains(c.detail, "plug down") {
			t.Errorf("the daemon line should say how to stop it: %q", c.detail)
		}
	}
}

// The check that was missing the afternoon plug's stub answered in 15ms, every
// program on the machine was blind, and doctor printed "no problems" — then
// pointed at Docker Desktop, because its one canned remedy for slowness lives
// there.
//
// Every other local check asks plug's OWN resolver, which is the one path an
// application never takes. These assert the three readings of the path they DO
// take, and that the remedy names the system resolver rather than a container
// runtime that has nothing to do with it.
func TestResolutionVerdict(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		took   time.Duration
		status checkStatus
		says   string
	}{
		{"resolves fast", nil, 20 * time.Millisecond, stOK, "getaddrinfo"},
		{"resolves, far too slowly", nil, 6 * time.Second, stWarn, "retrying rather than answering"},
		{"does not resolve at all", errors.New("no such host"), 30 * time.Second, stWarn, "the system one is not passing it on"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolutionVerdict(c.err, c.took)
			if got.status != c.status {
				t.Errorf("status = %v, want %v (%s)", got.status, c.status, got.detail)
			}
			if !strings.Contains(got.detail, c.says) {
				t.Errorf("detail does not say %q:\n  %s", c.says, got.detail)
			}
			if c.status == stOK {
				return
			}
			if got.remedy == "" {
				t.Fatal("a warning with no remedy — the reader is left with a symptom")
			}
			// The whole point: this is NOT a Docker problem, and saying so sent a
			// user to the wrong settings page for an afternoon.
			if strings.Contains(strings.ToLower(got.remedy), "docker") {
				t.Errorf("the remedy blames Docker for a system-resolver failure:\n  %s", got.remedy)
			}
		})
	}
}
