package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The --mount grammar: [<name>:]<volume-or-path>[:<local-path>], with a
// Windows drive path allowed as the local path even though it carries the
// separator.
func TestParseMount(t *testing.T) {
	cases := []struct {
		raw                string
		name, volume, path string
	}{
		{"/data", "", "/data", "/data"},
		{"/var/lib/pg/data", "", "/var/lib/pg/data", "/var/lib/pg/data"},
		{"data:/srv/data", "", "data", "/srv/data"},
		{"/data:/srv/data", "", "/data", "/srv/data"},
		{"api:data:/srv/data", "api", "data", "/srv/data"},
		{"api:/data:/srv/data", "api", "/data", "/srv/data"},
		{`data:C:\srv\data`, "", "data", filepath.Clean(`C:\srv\data`)},
		{`api:data:D:/srv/data`, "api", "data", filepath.Clean("D:/srv/data")},
		{"/data/", "", "/data/", "/data"},
	}
	for _, c := range cases {
		got, err := parseMount(c.raw)
		if err != nil {
			t.Errorf("%q: %v", c.raw, err)
			continue
		}
		// Clean on both sides: on Windows the separator is the other one.
		if got.name != c.name || got.volume != c.volume || got.path != filepath.Clean(c.path) {
			t.Errorf("%q: got %q/%q/%q, want %q/%q/%q", c.raw, got.name, got.volume, got.path, c.name, c.volume, c.path)
		}
	}
	bad := []string{"", "data", "data:relative", "Api:data:/x", "a:b:c:d", ":/x", "/a/../b:/x", "a b:/x"}
	for _, r := range bad {
		if _, err := parseMount(r); err == nil {
			t.Errorf("%q should be refused", r)
		}
	}
	// A volume NAME alone has nowhere to go, and the message says how to say it.
	if _, err := parseMount("data"); err == nil || !strings.Contains(err.Error(), "data:<local-path>") {
		t.Errorf("a bare name: %v", err)
	}
}

// An unnamed mount belongs to the one -s name, or to --env-of; two names and
// no --env-of is refused, not guessed; a named mount is left alone.
func TestResolveMountNames(t *testing.T) {
	one := []tunnel.ExposeSpec{{Name: "web", ClusterPort: "80", LocalPort: "3000"}, {Name: "web", ClusterPort: "443", LocalPort: "3443"}}
	two := append(one, tunnel.ExposeSpec{Name: "api", ClusterPort: "80", LocalPort: "4000"})
	specs := []mountSpec{{volume: "/data", path: "/data"}, {name: "db", volume: "pg", path: "/pg"}}

	got, err := resolveMountNames(specs, one, "")
	if err != nil || got[0].name != "web" || got[1].name != "db" {
		t.Fatalf("one -s name: %v %v", got, err)
	}
	got, err = resolveMountNames(specs, two, "orders")
	if err != nil || got[0].name != "orders" {
		t.Fatalf("--env-of wins: %v %v", got, err)
	}
	if _, err := resolveMountNames(specs, two, ""); err == nil || !strings.Contains(err.Error(), "several names") {
		t.Fatalf("two -s names, no --env-of: %v", err)
	}
	if _, err := resolveMountNames(specs, nil, ""); err == nil || !strings.Contains(err.Error(), "which workload") {
		t.Fatalf("nothing implied: %v", err)
	}
	if got, err := resolveMountNames(specs[1:], nil, ""); err != nil || got[0].name != "db" {
		t.Fatalf("a named mount needs nothing: %v %v", got, err)
	}
}

// What the agent answers, and what it refuses.
func TestParseMountReply(t *testing.T) {
	r, err := parseMountReply("mounted host=10.0.1.7 port=445 share=vol user=plug\n")
	if err != nil || r.host != "10.0.1.7" || r.port != "445" || r.share != "vol" || r.user != "plug" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := parseMountReply("error: \"web\" has no volume \"x\" — it mounts: /data (volume d)"); err == nil || !strings.HasPrefix(err.Error(), "\"web\" has no volume") {
		t.Fatalf("an agent error is the error: %v", err)
	}
	if _, err := parseMountReply("mounted host=1.2.3.4"); err == nil {
		t.Fatal("an incomplete reply is refused")
	}
	if _, err := parseMountReply("sh: mount-volume: not found"); err == nil {
		t.Fatal("an off-protocol answer is refused")
	}
	if p := mintMountPass(); len(p) != 40 || strings.ToLower(p) != p {
		t.Fatalf("pass = %q", p)
	}
}

// The local end: every connection accepted is carried to the current target
// through the dial the tunnel provides, and a retarget moves the next one.
func TestMountForwardCarriesToTarget(t *testing.T) {
	echo := func(banner string) string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					c.Write([]byte(banner))
					io.Copy(c, c)
				}()
			}
		}()
		t.Cleanup(func() { ln.Close() })
		return ln.Addr().String()
	}
	a, b := echo("A:"), echo("B:")
	dialed := make(chan string, 4)
	fw, err := newMountForward("127.0.0.1:0", a, func(addr string) (net.Conn, error) {
		dialed <- addr
		return net.Dial("tcp", addr)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	talk := func(want string) {
		c, err := net.Dial("tcp", fw.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte("ping"))
		buf := make([]byte, 16)
		n, _ := io.ReadAtLeast(c, buf, len(want)+4)
		if got := string(buf[:n]); got != want+"ping" {
			t.Fatalf("got %q, want %q", got, want+"ping")
		}
	}
	talk("A:")
	if <-dialed != a {
		t.Fatal("the first connection must reach the first target")
	}
	fw.Retarget(b)
	talk("B:")
	if <-dialed != b {
		t.Fatal("after a retarget the next connection must reach the new target")
	}
}

// Records: written under ~/.plug/mounts by the session, read back, and an
// orphan is one whose pid is gone.
func TestMountRecordsAndOrphans(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	spec := mountSpec{name: "web", volume: "/data", path: filepath.Join(dir, "mnt", "data")}
	unmark := markMounted(spec, "127.0.0.1:51234")
	recs := mountRecords()
	if len(recs) != 1 || recs[0].pid != os.Getpid() || recs[0].path != spec.path || recs[0].spec != spec.String() || recs[0].local != "127.0.0.1:51234" {
		t.Fatalf("records = %+v", recs)
	}
	if o := orphanMounts(recs, func(int) bool { return true }); len(o) != 0 {
		t.Fatal("a live pid is not an orphan")
	}
	if o := orphanMounts(recs, func(int) bool { return false }); len(o) != 1 {
		t.Fatal("a dead pid is an orphan")
	}
	unmark()
	if recs := mountRecords(); len(recs) != 0 {
		t.Fatalf("after the cleanup: %+v", recs)
	}
	// Two mounts, two records, distinct files.
	u1 := markMounted(mountSpec{name: "a", volume: "x", path: "/one"}, "l")
	u2 := markMounted(mountSpec{name: "a", volume: "y", path: "/two"}, "l")
	if len(mountRecords()) != 2 {
		t.Fatal("one record per mountpoint")
	}
	u1()
	u2()
}

// The launcher forwards --mount raw across the exec and the core strips it
// back, alongside the flags that already travel that way.
func TestMountCrossesTheExec(t *testing.T) {
	lead, rest, err := stripLeadingAll([]string{"-s", "web:80:3000", "--mount", "/data", "--env-of", "orders", "--mount", "api:pg:/pg", "npm", "start"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lead.mounts) != 2 || lead.mounts[0] != "/data" || lead.mounts[1] != "api:pg:/pg" {
		t.Fatalf("mounts = %v", lead.mounts)
	}
	if len(lead.specs) != 1 || lead.policy.from != "orders" || strings.Join(rest, " ") != "npm start" {
		t.Fatalf("the rest: %v %v %v", lead.specs, lead.policy, rest)
	}
	o, cmd := parseArgs([]string{"-c", "--env-of", "orders", "--mount", "/data", "--mount=pg:/pg", "python", "job.py"})
	if len(o.mounts) != 2 || o.mounts[1] != "pg:/pg" || strings.Join(cmd, " ") != "python job.py" {
		t.Fatalf("parseArgs: %v %v", o.mounts, cmd)
	}
}

// The doctor's verdict on stale mounts, without a mount: what --fix reports,
// and what a plain doctor says.
func TestMountsVerdict(t *testing.T) {
	old := doctorFix
	defer func() { doctorFix = old }()
	doctorFix = false
	c := mountsVerdict(0, []string{"/srv/data"})
	if c.status != stFail || !strings.Contains(c.detail, "/srv/data") || !strings.Contains(c.remedy, "--fix") {
		t.Fatalf("plain doctor: %+v", c)
	}
	doctorFix = true
	if c := mountsVerdict(2, nil); c.status != stOK || !strings.Contains(c.detail, "2 mount(s)") {
		t.Fatalf("all fixed: %+v", c)
	}
	if c := mountsVerdict(1, []string{"/busy"}); c.status != stFail || !strings.Contains(c.detail, "/busy") || !strings.Contains(c.remedy, "by hand") {
		t.Fatalf("partly fixed: %+v", c)
	}
}

// The automatic mounts: which workloads (the -s names and --env-of, once
// each), what --no-mount does to them, and the agent's volumes-of line.
func TestAutoMountPolicyAndNames(t *testing.T) {
	cfg := config{
		exposes:   []tunnel.ExposeSpec{{Name: "web"}, {Name: "web"}, {Name: "api"}},
		envPolicy: envPolicy{from: "orders"},
	}
	if got := strings.Join(autoMountNames(cfg), ","); got != "web,api,orders" {
		t.Fatalf("names = %q", got)
	}
	cfg.mountPolicy = parseNoMount("")
	if !cfg.mountPolicy.off || autoMountNames(cfg) != nil {
		t.Fatal("a bare --no-mount mounts nothing")
	}
	p := parseNoMount("/data, /var/lib/pg")
	if p.off || !p.drop["/data"] || !p.drop["/var/lib/pg"] || p.drop["/x"] {
		t.Fatalf("--no-mount=/a,/b: %+v", p)
	}
	if !looksLikePathList("/data") || !looksLikePathList("/a,/b") || looksLikePathList("npm") || looksLikePathList("/a,b") || looksLikePathList("") {
		t.Fatal("looksLikePathList")
	}
	o, cmd := parseArgs([]string{"-s", "web:80:3000", "--no-mount", "/data", "npm", "start"})
	if !o.noMount || o.noMountList != "/data" || strings.Join(cmd, " ") != "npm start" {
		t.Fatalf("parseArgs --no-mount /data: %+v %v", o, cmd)
	}
	o, cmd = parseArgs([]string{"-c", "--env-of", "orders", "--no-mount", "python", "job.py"})
	if !o.noMount || o.noMountList != "" || strings.Join(cmd, " ") != "python job.py" {
		t.Fatalf("parseArgs bare --no-mount: %+v %v", o, cmd)
	}
	lead, rest, err := stripLeadingAll([]string{"--no-mount", "/a,/b", "-c", "--no-mount", "make", "test"})
	if err != nil || !lead.noMount || lead.noMountList != "/a,/b" || strings.Join(rest, " ") != "make test" {
		t.Fatalf("strip: %+v %v %v", lead, rest, err)
	}
}

func TestWorkloadVolumesReply(t *testing.T) {
	if v, err := parseVolumesReply("volumes /data /var/lib/pg\n"); err != nil || strings.Join(v, ",") != "/data,/var/lib/pg" {
		t.Fatal(v, err)
	}
	if v, err := parseVolumesReply("volumes"); err != nil || len(v) != 0 {
		t.Fatal("no volumes")
	}
	if v, err := parseVolumesReply("error: unknown command \"volumes-of\""); err != nil || v != nil {
		t.Fatal("an old agent has no volumes, and that is not an error")
	}
	if _, err := parseVolumesReply("error: no Service \"x\""); err == nil {
		t.Fatal("an agent error is the error")
	}
	if _, err := parseVolumesReply("sh: volumes-of: not found"); err == nil {
		t.Fatal("an off-protocol answer is refused")
	}
}
