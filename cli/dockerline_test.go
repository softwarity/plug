package main

import (
	_ "embed"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// dockerRunHelpFixture is `docker run --help` as docker 29 prints it, which is
// what the line is read with: the table comes from the docker that runs the
// line, never from plug.
//
//go:embed testdata/docker-run-help.txt
var dockerRunHelpFixture string

func testDockerRunOptions(t *testing.T) *dockerRunOptions {
	t.Helper()
	opts := parseDockerRunHelp(dockerRunHelpFixture)
	if opts == nil {
		t.Fatal("no option read from the docker run --help fixture")
	}
	return opts
}

// The help says which options take a value by printing a type after them. A
// boolean read as valued would swallow the image, a valued one read as a
// boolean would make its value the image: both are a different container.
func TestTheDockerRunHelpSaysWhichOptionsTakeAValue(t *testing.T) {
	opts := testDockerRunOptions(t)
	for name, valued := range map[string]bool{
		"publish": true, "network": true, "volume": true, "mount": true, "env": true, "gpus": true, "ip6": true,
		"rm": false, "detach": false, "interactive": false, "tty": false, "init": false, "sig-proxy": false,
		"publish-all": false,
		// Newer than any table written by hand would be.
		"use-api-socket": false,
		// Hidden, and still accepted.
		"net": true, "net-alias": true,
	} {
		got, known := opts.valued[name]
		if !known {
			t.Errorf("--%s is not read from the help", name)
		} else if got != valued {
			t.Errorf("--%s read as taking a value: %v, want %v", name, got, valued)
		}
	}
	for short, long := range map[byte]string{'p': "publish", 'v': "volume", 'e': "env", 'h': "hostname", 'i': "interactive", 't': "tty", 'd': "detach", 'P': "publish-all"} {
		if opts.short[short] != long {
			t.Errorf("-%c is read as --%s, want --%s", short, opts.short[short], long)
		}
	}
	if parseDockerRunHelp("docker: 'run' is not a docker command.\n") != nil {
		t.Error("a help with no option in it gave a table")
	}
}

// The help of the docker on this machine reads like the fixture. Where there is
// no docker, there is nothing to compare.
func TestTheInstalledDockerHelpIsReadable(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI here")
	}
	out, err := exec.Command("docker", "run", "--help").Output()
	if err != nil {
		t.Skipf("docker run --help: %v", err)
	}
	opts := parseDockerRunHelp(string(out))
	if opts == nil || !opts.valued["publish"] || opts.valued["rm"] || opts.short['p'] != "publish" {
		t.Errorf("the installed docker's help is not read as the fixture is:\n%s", out)
	}
}

// What moves to the sidecar, what stays on the user's container, and which
// paths the user mounts. What stays is as the user wrote it; what moves is
// written long.
func TestTheLineIsSplitBetweenTheSidecarAndTheContainer(t *testing.T) {
	opts := testDockerRunOptions(t)
	for _, c := range []struct {
		line          string
		sidecar, user []string
		targets       []string
	}{
		{"-p 3000:3000 --rm -it grafana/grafana",
			[]string{"--publish=3000:3000"}, []string{"--rm", "-it", "grafana/grafana"}, nil},
		{"-itp 8080:80 nginx",
			[]string{"--publish=8080:80"}, []string{"--interactive", "--tty", "nginx"}, nil},
		{"-p8080:80 --publish=9090:90 nginx",
			[]string{"--publish=8080:80", "--publish=9090:90"}, []string{"nginx"}, nil},
		{"--network=my-net --name web img",
			[]string{"--network=my-net"}, []string{"--name", "web", "img"}, nil},
		{"--net my-net --net-alias api img",
			[]string{"--network=my-net", "--network-alias=api"}, []string{"img"}, nil},
		{"-h box --add-host db:10.0.0.9 --expose 8080 img",
			[]string{"--hostname=box", "--add-host=db:10.0.0.9", "--expose=8080"}, []string{"img"}, nil},
		// After the image, everything is the container's, -p included.
		{"-e A=1 -v ./d:/var/lib/grafana:rw img serve -p 1 --network x",
			nil, []string{"-e", "A=1", "-v", "./d:/var/lib/grafana:rw", "img", "serve", "-p", "1", "--network", "x"},
			[]string{"/var/lib/grafana"}},
		{"--mount type=bind,source=/x,target=/data/ --tmpfs /run:rw,size=64m -v /cache img",
			nil, []string{"--mount", "type=bind,source=/x,target=/data/", "--tmpfs", "/run:rw,size=64m", "-v", "/cache", "img"},
			[]string{"/data", "/run", "/cache"}},
		{`-v C:\Users\me\data:/data:ro img`,
			nil, []string{"-v", `C:\Users\me\data:/data:ro`, "img"}, []string{"/data"}},
		// A boolean the help knows does not take the image as its value.
		{"--use-api-socket --rm img cmd",
			nil, []string{"--use-api-socket", "--rm", "img", "cmd"}, nil},
		// An option this docker does not have is docker's to refuse.
		{"--no-such-option img",
			nil, []string{"--no-such-option", "img"}, nil},
		// -d is left out, alone, in a cluster or written long.
		{"-d --rm img", nil, []string{"--rm", "img"}, nil},
		{"-dit -p 80:80 img", []string{"--publish=80:80"}, []string{"--interactive", "--tty", "img"}, nil},
		{"--detach img", nil, []string{"img"}, nil},
		{"--detach=false img", nil, []string{"img"}, nil},
		{"-- img -p 1", nil, []string{"--", "img", "-p", "1"}, nil},
	} {
		got, err := readDockerRunLine(strings.Fields(c.line), opts)
		if err != nil {
			t.Errorf("%q was refused: %v", c.line, err)
			continue
		}
		if !reflect.DeepEqual(got.sidecar, c.sidecar) {
			t.Errorf("%q: the sidecar gets %q, want %q", c.line, got.sidecar, c.sidecar)
		}
		if !reflect.DeepEqual(got.user, c.user) {
			t.Errorf("%q: the container gets %q, want %q", c.line, got.user, c.user)
		}
		if !reflect.DeepEqual(got.targets, c.targets) {
			t.Errorf("%q: the user mounts %q, want %q", c.line, got.targets, c.targets)
		}
		if want := strings.Contains(c.line, "-d ") || strings.Contains(c.line, "-dit") || c.line == "--detach img"; got.detach != want {
			t.Errorf("%q: -d read as asked: %v, want %v", c.line, got.detach, want)
		}
	}
}

// What cannot be honoured is refused before anything starts, saying why and
// what to write instead.
func TestWhatTheSidecarCannotHonourIsRefused(t *testing.T) {
	opts := testDockerRunOptions(t)
	for _, c := range []struct{ line, mustSay string }{
		{"-P img", "-p 8080:8080"},
		{"--dns 1.1.1.1 img", "--dns"},
		{"--dns-search corp img", "--dns-search"},
		{"--network host img", "--network host"},
		{"--network=none img", "--network none"},
		{"--network container:other img", "--network container:other"},
		{"--rm -p", "no value"},
	} {
		_, err := readDockerRunLine(strings.Fields(c.line), opts)
		if err == nil {
			t.Errorf("%q was accepted", c.line)
		} else if !strings.Contains(err.Error(), c.mustSay) {
			t.Errorf("%q: the refusal does not say %q: %v", c.line, c.mustSay, err)
		}
	}
}

// The user's mount takes its path and everything under it; a path beside it,
// sharing only a prefix of letters, is not under it.
func TestTheUsersMountsTakeTheirPathsFromTheWorkload(t *testing.T) {
	targets := []string{"/var/lib/grafana", "/certificates"}
	for p, want := range map[string]bool{
		"/var/lib/grafana":         true,
		"/var/lib/grafana/":        true,
		"/var/lib/grafana/plugins": true,
		"/certificates/ca.crt":     true,
		"/var/lib/grafana-data":    false,
		"/var/lib":                 false,
		"/etc/grafana/grafana.ini": false,
	} {
		if got := mountedByUser(p, targets); got != want {
			t.Errorf("%s taken by the user's mounts: %v, want %v", p, got, want)
		}
	}
	if got := leaveUserMounts("web", []string{"/certificates/ca.crt", "/etc/app.conf"}, targets); !reflect.DeepEqual(got, []string{"/etc/app.conf"}) {
		t.Errorf("the workload's files left to the container: %q", got)
	}
}
