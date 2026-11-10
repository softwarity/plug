package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The command's arguments follow the files the way the variables do: a cluster
// path of the workload names the copy brought over. Every expectation is built
// with filepath.Join, so a Windows run checks backslashes, not a literal path.

// tlsSource is a workload that mounts /etc/tls and brought tls.crt over (and
// not tls.key): the case that asked for the feature, with one file missing.
func tlsSource(t *testing.T, name string) argSource {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etc", "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "tls", "tls.crt"), []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	return argSource{name: name, dir: dir, paths: []string{"/etc/tls"}}
}

func TestRepointArgsShapes(t *testing.T) {
	src := tlsSource(t, "ui")
	crt := filepath.Join(src.dir, "/etc/tls/tls.crt")
	args := []string{
		"polyglot",
		"/etc/tls/tls.crt",               // the whole argument
		"--ssl-cert=/etc/tls/tls.crt",    // --opt=value
		"-Djavax.ssl=/etc/tls/tls.crt",   // -Dkey=value: after the FIRST =
		"--ssl-cert", "/etc/tls/tls.crt", // the separate value, the whole-argument case
		"/etc/other/x.pem",                // not mounted: left alone
		"--x=/etc/tlsx/a",                 // not a path boundary: left alone
		"--url=https://h/etc/tls/tls.crt", // a substring: never touched
		"sh -c 'cat /etc/tls/tls.crt'",    // a shell line: never parsed
		"etc/tls/tls.crt",                 // relative: not a cluster path
		"KEY=/etc/tls/tls.crt",            // not a dashed option: left alone
		"--ssl-key=/etc/tls/tls.key",      // mounted, but not brought over
	}
	got, hits := repointArgs(args, []argSource{src})
	want := []string{
		"polyglot",
		crt,
		"--ssl-cert=" + crt,
		"-Djavax.ssl=" + crt,
		"--ssl-cert", crt,
		"/etc/other/x.pem",
		"--x=/etc/tlsx/a",
		"--url=https://h/etc/tls/tls.crt",
		"sh -c 'cat /etc/tls/tls.crt'",
		"etc/tls/tls.crt",
		"KEY=/etc/tls/tls.crt",
		"--ssl-key=/etc/tls/tls.key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	var repointed, missing []string
	for _, h := range hits {
		if h.workload != "ui" || h.mountedBy != nil {
			t.Fatalf("one workload: %+v", h)
		}
		if h.missing {
			missing = append(missing, h.before)
		} else {
			repointed = append(repointed, h.before)
		}
	}
	if len(repointed) != 4 || !reflect.DeepEqual(missing, []string{"--ssl-key=/etc/tls/tls.key"}) {
		t.Fatalf("repointed %q, missing %q", repointed, missing)
	}
	// No source: the very same slice comes back, nothing looked at.
	if got, hits := repointArgs(args, nil); !reflect.DeepEqual(got, args) || hits != nil {
		t.Fatal("no source must be a no-op")
	}
}

// A live volume is named whether or not the file under it exists yet (a log
// the process is about to create), and the longest cluster path wins between a
// workload's files and its volumes.
func TestRepointArgsVolumes(t *testing.T) {
	src := tlsSource(t, "api")
	src.paths = []string{"/data/tls"}
	if err := os.MkdirAll(filepath.Join(src.dir, "data", "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.dir, "data", "tls", "ca"), []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	vol := t.TempDir()
	src.mounts = []volumeMount{{cluster: "/data", local: vol}}
	got, hits := repointArgs([]string{"--log=/data/logs/new.log", "/data", "--ca=/data/tls/ca"}, []argSource{src})
	want := []string{
		"--log=" + filepath.Join(vol, "logs", "new.log"),
		vol,
		"--ca=" + filepath.Join(src.dir, "/data/tls/ca"),
	}
	if !reflect.DeepEqual(got, want) || len(hits) != 3 {
		t.Fatalf("got %q (%d hits)\nwant %q", got, len(hits), want)
	}
}

// Two workloads: each path is looked up in the one that mounts it, and when
// both do, the first one (the -s order) is used and named; a first one that
// did not bring the file over gives way to one that did.
func TestRepointArgsTwoWorkloads(t *testing.T) {
	ui := tlsSource(t, "ui")
	api := tlsSource(t, "api")
	api.paths = []string{"/etc/tls", "/etc/api"}
	if err := os.MkdirAll(filepath.Join(api.dir, "etc", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api.dir, "etc", "api", "key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api.dir, "etc", "tls", "tls.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, hits := repointArgs([]string{"/etc/tls/tls.crt", "/etc/api/key", "/etc/tls/tls.key"}, []argSource{ui, api})
	want := []string{
		filepath.Join(ui.dir, "/etc/tls/tls.crt"),
		filepath.Join(api.dir, "/etc/api/key"),
		filepath.Join(api.dir, "/etc/tls/tls.key"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if hits[0].workload != "ui" || !reflect.DeepEqual(hits[0].mountedBy, []string{"ui", "api"}) {
		t.Fatalf("both mount /etc/tls, ui comes first: %+v", hits[0])
	}
	if hits[1].workload != "api" || hits[1].mountedBy != nil {
		t.Fatalf("only api mounts /etc/api: %+v", hits[1])
	}
	if hits[2].workload != "api" || hits[2].missing {
		t.Fatalf("ui did not bring tls.key over, api did: %+v", hits[2])
	}
}

// {PORT} is substituted first, by the launcher's core, and a port is not a
// path: in the same command, --x={PORT} stays a port and --y=/etc/tls/… is
// repointed.
func TestRepointArgsAfterPortVars(t *testing.T) {
	src := tlsSource(t, "ui")
	specs := []tunnel.ExposeSpec{{Name: "ui", ClusterPort: "443", PortVar: "PORT"}}
	specs, args, err := resolvePortVars(specs, []string{"polyglot", "--x={PORT}", "--y=/etc/tls/tls.crt"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := repointArgs(args, []argSource{src})
	want := []string{"polyglot", "--x=" + specs[0].LocalPort, "--y=" + filepath.Join(src.dir, "/etc/tls/tls.crt")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// The session's half: the sources come from what the projections recorded,
// the -s order first; what happened is said; --no-repoint leaves the arguments
// as written and says how many it left; the --dockerrun sidecar is never
// touched.
func TestRepointSessionArgs(t *testing.T) {
	ui := tlsSource(t, "ui")
	recordProjectedFiles("ui", ui.dir, ui.paths)
	t.Cleanup(func() {
		projectedFiles.Lock()
		delete(projectedFiles.by, "ui")
		projectedFiles.Unlock()
	})
	cfg := config{exposes: []tunnel.ExposeSpec{{Name: "ui", ClusterPort: "443", LocalPort: "3000"}}}
	args := []string{"polyglot", "--ssl-cert=/etc/tls/tls.crt", "--ssl-key=/etc/tls/tls.key"}

	done := captureStderr(t)
	got := repointSessionArgs(cfg, args)
	said := done()
	if got[1] != "--ssl-cert="+filepath.Join(ui.dir, "/etc/tls/tls.crt") || got[2] != args[2] {
		t.Fatalf("got %q", got)
	}
	for _, line := range []string{
		"ui: 1 argument(s) repointed",
		"ui: argument --ssl-key=/etc/tls/tls.key keeps the pod's path: the file it names was not brought over",
	} {
		if !strings.Contains(said, line) {
			t.Fatalf("missing %q in:\n%s", line, said)
		}
	}

	cfg.noRepoint = true
	done = captureStderr(t)
	got = repointSessionArgs(cfg, args)
	said = done()
	if !reflect.DeepEqual(got, args) || !strings.Contains(said, "--no-repoint: 1 argument(s)") {
		t.Fatalf("--no-repoint: got %q, said %q", got, said)
	}

	cfg.noRepoint, cfg.sidecar = false, true
	if got := repointSessionArgs(cfg, args); !reflect.DeepEqual(got, args) {
		t.Fatalf("the --dockerrun sidecar: got %q", got)
	}

	// A workload nothing was recorded for contributes nothing.
	if got := repointSessionArgs(config{envPolicy: envPolicy{from: "other"}}, args); !reflect.DeepEqual(got, args) {
		t.Fatalf("no projection: got %q", got)
	}
}

// --no-repoint crosses from the launcher to the core like every other flag:
// parsed in any order, encoded at the head of the core's argv, stripped back.
func TestNoRepointFlagTravels(t *testing.T) {
	o, cmd := parseArgs([]string{"--no-repoint", "-s", "ui:443:PORT", "polyglot", "--port={PORT}"})
	if !o.noRepoint || strings.Join(cmd, " ") != "polyglot --port={PORT}" {
		t.Fatalf("parseArgs: %+v %v", o, cmd)
	}
	lead, rest, err := stripLeadingAll(encodeCoreArgv(o, cmd))
	if err != nil || !lead.noRepoint || len(lead.specs) != 1 || strings.Join(rest, " ") != "polyglot --port={PORT}" {
		t.Fatalf("round trip: %+v %v %v", lead, rest, err)
	}
	if lead, _, _ := stripLeadingAll([]string{"-s", "ui:443:3000", "polyglot"}); lead.noRepoint {
		t.Fatal("absent, the flag must stay off")
	}
	if cfg := finalizeInProcess(config{}, o); !cfg.noRepoint {
		t.Fatal("finalizeInProcess drops --no-repoint")
	}
}
