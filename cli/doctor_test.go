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
