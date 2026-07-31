package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// sandboxHome points plugDir() at a temp directory. UserHomeDir reads $HOME on
// unix and %USERPROFILE% on Windows — set both rather than assume the platform.
func sandboxHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// The record exists so a session refused on a taken name can point at the
// process holding it — with enough on screen for a human to recognise it before
// killing anything.
func TestMarkServedThenHolderNamesThisProcess(t *testing.T) {
	sandboxHome(t)

	forget := markServed("fpl-svc", []string{"-s", "fpl-svc:3000:PORT", "nest", "start", "--watch"})
	holder := servedHolder("fpl-svc")
	if holder == "" {
		t.Fatal("servedHolder found nothing right after markServed")
	}
	for _, want := range []string{
		strconv.Itoa(os.Getpid()), // which process to look at
		"nest start --watch",      // and how to recognise it as yours
		"kill ",                   // and how to free the name
	} {
		if !strings.Contains(holder, want) {
			t.Errorf("holder text is missing %q:\n%s", want, holder)
		}
	}

	// A name this session never served says nothing about anyone.
	if other := servedHolder("fpl-ui"); other != "" {
		t.Errorf("servedHolder(unserved name) = %q, want empty", other)
	}

	// Teardown forgets it: the next session must not be told about a name that
	// is now free.
	forget()
	if after := servedHolder("fpl-svc"); after != "" {
		t.Errorf("record survived teardown: %q", after)
	}
}

// markServed must never be able to stop a session from starting.
func TestMarkServedSurvivesAnUnwritableHome(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/plug-test/nowhere")
	t.Setenv("USERPROFILE", "/nonexistent/plug-test/nowhere")
	forget := markServed("fpl-svc", []string{"-s", "fpl-svc:3000:PORT", "true"})
	if forget == nil {
		t.Fatal("markServed returned a nil cleanup")
	}
	forget() // must not panic
	if h := servedHolder("fpl-svc"); h != "" {
		t.Errorf("holder from an unwritable home = %q, want empty", h)
	}
}
