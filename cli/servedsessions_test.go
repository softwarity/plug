package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What doctor needs to put a session under its profile and probe its name has
// to survive the round trip through ~/.plug/served.
func TestTheServedRecordSaysWhereTheNameIsServed(t *testing.T) {
	sandboxHome(t)
	at := servedAt{cluster: "10.0.0.9:2222", profile: "dev",
		ports: []servedPort{{cluster: "8080", local: "3000"}, {cluster: "25", local: "2525"}}}
	forget := markServed("orders-svc", "40001", []string{"-s", "orders-svc:8080:3000", "npm", "start"}, at)
	defer forget()

	h := servedHolder("orders-svc")
	if h == nil {
		t.Fatal("servedHolder found nothing right after markServed")
	}
	if h.cluster != at.cluster || h.profile != at.profile {
		t.Errorf("record says cluster %q profile %q, want %q %q", h.cluster, h.profile, at.cluster, at.profile)
	}
	if len(h.ports) != 2 || h.ports[0] != at.ports[0] || h.ports[1] != at.ports[1] {
		t.Errorf("record ports = %+v, want %+v", h.ports, at.ports)
	}
}

// A record written by an older core has no cluster and no ports: still a live
// session holding a name, listed, never probed.
func TestARecordWithoutItsPlaceIsStillASession(t *testing.T) {
	sandboxHome(t)
	if err := os.MkdirAll(servedDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "pid = " + strconv.Itoa(os.Getpid()) + "\nport = 40001\ndir = x\ncmd = nest start\n"
	if err := os.WriteFile(filepath.Join(servedDir(), "orders-svc"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := listServed()
	if len(got) != 1 || got[0].name != "orders-svc" {
		t.Fatalf("listServed = %+v, want the one session", got)
	}
	if got[0].rec.cluster != "" || len(got[0].rec.ports) != 0 {
		t.Errorf("an older record read as %+v, want no cluster and no ports", got[0].rec)
	}
	var lines []check
	doctorServedElsewhere(func(c check) { lines = append(lines, c) })
	if len(lines) != 1 || lines[0].status != stSkip || !strings.Contains(lines[0].detail, "recorded no cluster") {
		t.Errorf("an older session is listed as %+v, want one unchecked line under local", lines)
	}
}

// Only live sessions are listed, by name, and nothing that is not a record.
func TestListServedKeepsTheLiveSessionsOnly(t *testing.T) {
	sandboxHome(t)
	if err := os.MkdirAll(servedDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, pid int) {
		body := "pid = " + strconv.Itoa(pid) + "\nport = 40001\n"
		if err := os.WriteFile(filepath.Join(servedDir(), name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("web", os.Getpid())
	write("api", os.Getpid())
	write("gone", 0)                 // a process that does not exist
	write("Not_A_Name", os.Getpid()) // not something -s could have served
	var names []string
	for _, e := range listServed() {
		names = append(names, e.name)
	}
	if strings.Join(names, ",") != "api,web" {
		t.Errorf("listServed = %v, want [api web]", names)
	}
}

func TestParseServedPortsDropsWhatIsNotAPair(t *testing.T) {
	got := parseServedPorts("8080:3000, 25:2525,bad,:1,2:,x:y")
	want := []servedPort{{"8080", "3000"}, {"25", "2525"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseServedPorts = %+v, want %+v", got, want)
	}
}

func TestAServedRecordGoesUnderItsProfile(t *testing.T) {
	profiles := map[string]bool{"dev": true, "dev-alias": true}
	for _, c := range []struct {
		why      string
		r        servedRecord
		profile  string
		hostport string
		want     bool
	}{
		{"the profile it named", servedRecord{servedAt: servedAt{cluster: "h:2222", profile: "dev"}}, "dev", "h:2222", true},
		{"not a second profile on the same agent", servedRecord{servedAt: servedAt{cluster: "h:2222", profile: "dev"}}, "dev-alias", "h:2222", false},
		{"--host alone: every profile on that agent", servedRecord{servedAt: servedAt{cluster: "h:2222"}}, "dev-alias", "h:2222", true},
		{"a profile since removed: by its agent", servedRecord{servedAt: servedAt{cluster: "h:2222", profile: "old"}}, "dev", "h:2222", true},
		{"another agent", servedRecord{servedAt: servedAt{cluster: "other:2222"}}, "dev", "h:2222", false},
		{"no cluster recorded", servedRecord{}, "dev", "h:2222", false},
	} {
		if got := servedBelongsTo(&c.r, c.profile, c.hostport, profiles); got != c.want {
			t.Errorf("%s: servedBelongsTo = %v, want %v", c.why, got, c.want)
		}
	}
}

func TestServedVerdict(t *testing.T) {
	r := &servedRecord{pid: 4242, cmd: "npm start", age: 5 * time.Minute}
	p := servedPort{cluster: "8080", local: "3000"}
	down := errors.New("refused")
	for _, c := range []struct {
		why    string
		probes []portProbe
		want   checkStatus
		has    string
	}{
		{"both answer", []portProbe{{port: p}}, stOK, "web:8080 -> localhost:3000 answers"},
		{"the cluster does not answer", []portProbe{{port: p, clusterErr: down, localErr: down}}, stFail, "web:8080 does not answer in the cluster"},
		{"nothing listens here", []portProbe{{port: p, localErr: down}}, stWarn, "nothing listens on localhost:3000"},
		{"not checked", nil, stSkip, "not checked: no tunnel"},
	} {
		got := servedVerdict("dev", "web", r, c.probes, "no tunnel")
		if got.status != c.want || !strings.Contains(got.detail, c.has) {
			t.Errorf("%s: %v %q, want %v containing %q", c.why, got.status, got.detail, c.want, c.has)
		}
		if !strings.Contains(got.detail, "PID 4242") || !strings.Contains(got.detail, "npm start") {
			t.Errorf("%s: the line does not say which session it is: %q", c.why, got.detail)
		}
		if (got.status == stFail || got.status == stWarn) && got.remedy == "" {
			t.Errorf("%s: a finding with no remedy", c.why)
		}
	}
}

// fakeClusterDial stands for the tunnel: it answers for the names it knows.
type fakeClusterDial map[string]bool

func (f fakeClusterDial) DialClusterTimeout(addr string, _ time.Duration) (net.Conn, error) {
	if !f[addr] {
		return nil, errors.New("connect: connection refused")
	}
	a, b := net.Pipe()
	b.Close()
	return a, nil
}

// End to end on this machine: a record, a listener standing for the session's
// process, the cluster half faked. The name is probed under its profile only.
func TestDoctorServedProbesBothHalves(t *testing.T) {
	sandboxHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, local, _ := net.SplitHostPort(ln.Addr().String())
	// A port nothing listens on: one just released.
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, deaf, _ := net.SplitHostPort(spare.Addr().String())
	spare.Close()

	at := func(l string) servedAt {
		return servedAt{cluster: "h:2222", ports: []servedPort{{cluster: "8080", local: l}}}
	}
	defer markServed("web", "40001", []string{"npm", "start"}, at(local))()
	defer markServed("api", "40002", []string{"npm", "run", "api"}, at(deaf))()

	var lines []check
	add := func(c check) { lines = append(lines, c) }
	doctorServed("dev", "h:2222", fakeClusterDial{"web:8080": true, "api:8080": true}, add)
	if len(lines) != 2 {
		t.Fatalf("doctorServed added %d lines, want 2: %+v", len(lines), lines)
	}
	if lines[0].name != "served api" || lines[0].status != stWarn {
		t.Errorf("api, whose process does not listen: %+v", lines[0])
	}
	if lines[1].name != "served web" || lines[1].status != stOK {
		t.Errorf("web, answered end to end: %+v", lines[1])
	}

	lines = nil
	doctorServed("dev", "h:2222", nil, add)
	for _, c := range lines {
		if c.status != stSkip {
			t.Errorf("without a tunnel nothing is probed: %+v", c)
		}
	}

	lines = nil
	doctorServed("prod", "other:2222", fakeClusterDial{}, add)
	if len(lines) != 0 {
		t.Errorf("sessions of another cluster listed under it: %+v", lines)
	}
}
