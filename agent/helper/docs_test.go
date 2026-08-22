package main

import (
	"os"
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
		{"the CD page", docContinuousDeployment, nil, "https://softwarity.github.io/plug/continuous-deployment"},
		{"a section", docKubernetes, []string{"names"}, "https://softwarity.github.io/plug/kubernetes#names"},
	}
	for _, c := range cases {
		if got := docURL(c.page, c.anchor...); got != c.want {
			t.Errorf("%s: docURL(%q, %v) = %q, want %q", c.name, c.page, c.anchor, got, c.want)
		}
	}
}

// Same guard as the CLI's: one place for the domain, or the link rots unnoticed.
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
			t.Errorf("%s hard-codes %s — build the link with docURL(...)", name, host)
		}
	}
}
