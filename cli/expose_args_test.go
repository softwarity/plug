package main

import (
	"reflect"
	"testing"
)

// The -s grammar and its trip across the launcher→core exec boundary are an
// INTER-VERSION protocol (an old launcher forwards the flags without
// understanding them; the core strips them back) — lock the wire format down.

func TestParseExpose(t *testing.T) {
	ok := []struct{ in, name, cport, lport string }{
		{"service1:8081:4200", "service1", "8081", "4200"},
		{"a:1:65535", "a", "1", "65535"},
	}
	for _, c := range ok {
		spec, err := parseExpose(c.in)
		if err != nil {
			t.Fatalf("parseExpose(%q): %v", c.in, err)
		}
		if spec.Name != c.name || spec.ClusterPort != c.cport || spec.LocalPort != c.lport {
			t.Fatalf("parseExpose(%q) = %+v", c.in, spec)
		}
	}
	for _, bad := range []string{"", "service1", "service1:8081", "s:0:1", "s:1:0",
		"s:65536:80", "s:x:80", ":8081:4200", "s:8081:4200:extra"} {
		if _, err := parseExpose(bad); err == nil {
			t.Fatalf("parseExpose(%q) should fail", bad)
		}
	}
}

func TestParseArgsServe(t *testing.T) {
	// New launcher: -s values are collected raw, the command is untouched.
	o, cmd := parseArgs([]string{"-p", "x", "-s", "a:1:2", "--serve", "b:3:4", "npm", "start"})
	if o.profile != "x" || !reflect.DeepEqual(o.exposes, []string{"a:1:2", "b:3:4"}) {
		t.Fatalf("opts = %+v", o)
	}
	if !reflect.DeepEqual(cmd, []string{"npm", "start"}) {
		t.Fatalf("cmd = %v", cmd)
	}

	// Old-launcher contract: parseArgs STOPS at the first unknown flag and
	// hands the whole tail to the core — that is how a pre--s launcher forwards
	// -s without understanding it. A launcher build must never break this.
	oldTail := []string{"-s", "a:1:2", "npm", "start"}
	o2, cmd2 := parseArgs(append([]string{"-p", "x", "--unknown-future-flag"}, oldTail...))
	if o2.profile != "x" {
		t.Fatalf("opts2 = %+v", o2)
	}
	if !reflect.DeepEqual(cmd2, append([]string{"--unknown-future-flag"}, oldTail...)) {
		t.Fatalf("cmd2 = %v", cmd2)
	}
}

func TestStripLeadingExposes(t *testing.T) {
	specs, rest, err := stripLeadingExposes([]string{"-s", "a:1:2", "--serve", "b:3:4", "npm", "start"})
	if err != nil || len(specs) != 2 || specs[0].Name != "a" || specs[1].Name != "b" {
		t.Fatalf("specs = %+v, err = %v", specs, err)
	}
	if !reflect.DeepEqual(rest, []string{"npm", "start"}) {
		t.Fatalf("rest = %v", rest)
	}

	// No -s at the head: everything is the command (the normal case).
	specs, rest, err = stripLeadingExposes([]string{"npm", "start", "-s"})
	if err != nil || specs != nil || !reflect.DeepEqual(rest, []string{"npm", "start", "-s"}) {
		t.Fatalf("specs = %v rest = %v err = %v", specs, rest, err)
	}

	// Invalid value fails loud — never silently swallowed into the command.
	if _, _, err = stripLeadingExposes([]string{"-s", "garbage", "npm"}); err == nil {
		t.Fatal("invalid -s value should fail")
	}
}
