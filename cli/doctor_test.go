package main

import (
	"net/url"
	"strings"
	"testing"
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
func TestOnlyAWedgedDatapathSendsYouToPlugDown(t *testing.T) {
	c, show := datapathVerdict(true, false)
	if !show {
		t.Fatal("an alive-but-mute datapath must be reported")
	}
	if c.status != stFail {
		t.Errorf("status = %v, want a failure — cluster names cannot resolve in this state", c.status)
	}
	if !strings.Contains(c.remedy, "plug down") {
		t.Errorf("remedy = %q — this is the one case where plug down is the answer", c.remedy)
	}
	if !strings.Contains(c.detail, "will not stop on its own") {
		t.Errorf("detail = %q, want it to say why waiting does not help", c.detail)
	}
}

// The healthy case must stay quiet about that command, or we are back to
// teaching people to use a teardown as routine maintenance.
func TestAWorkingDatapathNeverMentionsPlugDown(t *testing.T) {
	c, show := datapathVerdict(true, true)
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
	if _, show := datapathVerdict(false, false); show {
		t.Error("with no daemon running, this check must stay silent")
	}
}
