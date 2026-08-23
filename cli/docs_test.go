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
		{"a page", docKubernetes, nil, "https://softwarity.github.io/plug/#/kubernetes"},
		{"the CD page", docContinuousDeployment, nil, "https://softwarity.github.io/plug/#/continuous-deployment"},
		{"a section", docKubernetes, []string{"names"}, "https://softwarity.github.io/plug/#/kubernetes#names"},
		{"empty anchor is no anchor", docSwarm, []string{""}, "https://softwarity.github.io/plug/#/swarm"},
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
//
// This checks the PAGE exists; TestDocURLMatchesHowTheSiteRoutes checks the
// URL's SHAPE. Both are needed, and having only the first is how every link
// shipped pointing at the home page: each name was a real route, spelled in an
// address the site does not serve.
func TestDocPagesAreRealRoutes(t *testing.T) {
	routes := filepath.Join("..", "docs", "src", "app", "app.routes.ts")
	b, err := os.ReadFile(routes)
	if err != nil {
		t.Skipf("doc site not present (%v) — nothing to check against", err)
	}
	src := string(b)
	for _, page := range []string{docKubernetes, docSwarm, docSecurity, docTroubleshooting, docProfiles, docContinuousDeployment} {
		if !strings.Contains(src, "path: '"+page+"'") {
			t.Errorf("docs.go points at %q, which %s does not route", page, routes)
		}
	}
}

// The doc site serves its own copy of deploy/plug-k8s.yaml, so users can
// download the manifest from the page they are reading. Two copies of one file
// drift silently: this one had, and the site was handing out a manifest whose
// comments described an agent that no longer existed. Compare them, and let a
// build fail rather than a reader be misled.
func TestTheSiteServesTheRealManifest(t *testing.T) {
	source := filepath.Join("..", "deploy", "plug-k8s.yaml")
	served := filepath.Join("..", "docs", "src", "assets", "plug-k8s.yaml")

	a, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("manifest not present (%v)", err)
	}
	b, err := os.ReadFile(served)
	if err != nil {
		t.Skipf("doc site not present (%v)", err)
	}
	if string(a) != string(b) {
		t.Errorf("%s and %s have diverged - copy the first over the second", source, served)
	}
}

// The site routes on the hash (app.config.ts: withHashLocation), so an address
// without "#/" is not one it serves - GitHub Pages 404s, the SPA fallback boots,
// and the router lands on the home page because there is no fragment to read.
// Silent, and wrong for every link in every message.
func TestDocURLMatchesHowTheSiteRoutes(t *testing.T) {
	cfg := filepath.Join("..", "docs", "src", "app", "app.config.ts")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Skipf("doc site not present (%v)", err)
	}
	hashRouted := strings.Contains(string(b), "withHashLocation")
	got := docURL(docKubernetes)
	if hashRouted && !strings.Contains(got, "/#/") {
		t.Errorf("the site is hash-routed but docURL builds %q, which resolves to the home page", got)
	}
	if !hashRouted && strings.Contains(got, "/#/") {
		t.Errorf("the site is path-routed but docURL builds %q", got)
	}
}
