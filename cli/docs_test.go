package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocURL(t *testing.T) {
	cases := []struct {
		name   string
		page   string
		anchor []string
		want   string
	}{
		{"home", docHome, nil, "https://softwarity.github.io/plug/"},
		{"a page", docKubernetes, nil, "https://softwarity.github.io/plug/kubernetes"},
		{"a section", docKubernetes, []string{anchorGitOps}, "https://softwarity.github.io/plug/kubernetes#gitops"},
		{"empty anchor is no anchor", docSwarm, []string{""}, "https://softwarity.github.io/plug/swarm"},
	}
	for _, c := range cases {
		if got := docURL(c.page, c.anchor...); got != c.want {
			t.Errorf("%s: docURL(%q, %v) = %q, want %q", c.name, c.page, c.anchor, got, c.want)
		}
	}
}

// The point of docs.go is that a domain change is ONE edit. A link written
// straight into a message defeats that silently — nothing breaks, it just rots
// until someone follows it.
func TestNoHardCodedDocLinks(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	const host = "softwarity.github.io"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "docs.go" || name == "docs_test.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(b), host) {
			t.Errorf("%s hard-codes %s — build the link with docURL(...) so the domain lives in one place", name, host)
		}
	}
}

// A link to a page the site does not route is worse than no link: it lands on
// the SPA fallback and the reader is left looking. The routes file is the source
// of truth, so read it rather than restate it.
func TestDocPagesAreRealRoutes(t *testing.T) {
	routes := filepath.Join("..", "docs", "src", "app", "app.routes.ts")
	b, err := os.ReadFile(routes)
	if err != nil {
		t.Skipf("doc site not present (%v) — nothing to check against", err)
	}
	src := string(b)
	for _, page := range []string{docKubernetes, docSwarm, docSecurity, docTroubleshooting, docProfiles} {
		if !strings.Contains(src, "path: '"+page+"'") {
			t.Errorf("docs.go points at %q, which %s does not route", page, routes)
		}
	}
}
