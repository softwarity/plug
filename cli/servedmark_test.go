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

	forget := markServed("fpl-svc", "40001", []string{"-s", "fpl-svc:3000:PORT", "nest", "start", "--watch"})
	h := servedHolder("fpl-svc")
	if h == nil {
		t.Fatal("servedHolder found nothing right after markServed")
	}
	if h.pid != os.Getpid() {
		t.Errorf("record names PID %d, want this process (%d)", h.pid, os.Getpid())
	}
	// The port is what proves this record IS the holder rather than a leftover
	// naming a recycled PID — without it we could offer to kill an innocent one.
	if h.port != "40001" {
		t.Errorf("record port = %q, want 40001", h.port)
	}
	for _, want := range []string{
		strconv.Itoa(os.Getpid()), // which process to look at
		"nest start --watch",      // and how to recognise it as yours
	} {
		if !strings.Contains(h.describe(), want) {
			t.Errorf("description is missing %q:\n%s", want, h.describe())
		}
	}

	// A name this session never served says nothing about anyone.
	if other := servedHolder("fpl-ui"); other != nil {
		t.Errorf("servedHolder(unserved name) = %+v, want nil", other)
	}

	// Teardown forgets it: the next session must not be told about a name that
	// is now free.
	forget()
	if after := servedHolder("fpl-svc"); after != nil {
		t.Errorf("record survived teardown: %+v", after)
	}
}

// markServed must never be able to stop a session from starting.
func TestMarkServedSurvivesAnUnwritableHome(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/plug-test/nowhere")
	t.Setenv("USERPROFILE", "/nonexistent/plug-test/nowhere")
	forget := markServed("fpl-svc", "40001", []string{"-s", "fpl-svc:3000:PORT", "true"})
	if forget == nil {
		t.Fatal("markServed returned a nil cleanup")
	}
	forget() // must not panic
	if h := servedHolder("fpl-svc"); h != nil {
		t.Errorf("holder from an unwritable home = %+v, want nil", h)
	}
}
