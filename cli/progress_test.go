package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// Off a terminal (a CI log, a redirect) the progress is plain lines: the
// label once, a step only when it changes, the final word with the time.
func TestProgressOffTerminalIsPlainLines(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	p := startProgress("mounting /data of geo")
	if p.tty {
		t.Skip("stderr is a terminal here")
	}
	p.step("helper starting")
	p.step("helper starting") // repeated: not printed again
	p.step("helper pending: no suitable node")
	p.done("mounted at /tmp/x, read-write, live")
	p.done("twice is once")
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()
	if strings.Count(out, "helper starting") != 1 || !strings.Contains(out, "no suitable node") {
		t.Fatalf("steps: %q", out)
	}
	if strings.Count(out, "mounted at /tmp/x") != 1 || strings.Contains(out, "twice") {
		t.Fatalf("done: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("no carriage return off a terminal: %q", out)
	}
}
