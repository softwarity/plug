package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// --dockerrun has no e2e on macOS and Windows: no hosted runner of either has
// a docker daemon for Linux containers, so the CI cell skips there. What those
// two OSes do differently is on THIS side of docker, though, and runs without
// a daemon: the docker CLI found in $PATH (docker.exe on Windows), the host
// paths handed to -v (a drive letter and backslashes on Windows, the
// per-user temp directory on macOS), the address the daemon's kernel uses to
// reach this host for a cifs volume (host.docker.internal on Docker Desktop),
// the exit status carried back. These tests run the real code against a fake
// docker that answers like the CLI and records what it was asked, so the
// macOS and Windows Test jobs exercise those branches on their own OS.
//
// The fake is this test binary itself, copied under the name docker: the init
// below turns it into the fake when fakeDockerEnv names a log, before the
// testing package parses anything. Copying a binary the test already has
// works the same on all three OSes, where a shell script would not run on
// Windows and a compiled helper would cost a go build per run.

// fakeDockerEnv names the log the fake appends one JSON line to per call. Set
// only by installFakeDocker, with t.Setenv, which keeps every parallel test
// from running while it is set.
const fakeDockerEnv = "PLUG_TEST_FAKE_DOCKER_LOG"

// fakeDockerExitEnv is the status the fake gives the user's container.
const fakeDockerExitEnv = "PLUG_TEST_FAKE_DOCKER_EXIT"

// fakeDockerHoldEnv names a file: the user's container writes <file>.started
// once running, then runs until <file> exists or a SIGTERM reaches it, so a
// test can signal plug while a container is up.
const fakeDockerHoldEnv = "PLUG_TEST_FAKE_DOCKER_HOLD"

func init() {
	if log := os.Getenv(fakeDockerEnv); log != "" {
		os.Exit(fakeDocker(log, os.Args[1:]))
	}
}

// fakeCall is one invocation of the fake: its argv, and for the user's
// container the resolver file it was handed, read through the host path the
// way the daemon would read it.
type fakeCall struct {
	Args   []string `json:"args"`
	Resolv string   `json:"resolv,omitempty"`
	Read   string   `json:"read,omitempty"` // why the resolver could not be read
	// For the containers: the process group the fake ran in (0 where the OS
	// has none to compare), and for the sidecar, that its stdin reached EOF.
	Pgrp    int  `json:"pgrp,omitempty"`
	HoldEOF bool `json:"holdEOF,omitempty"`
	// For the user's container held by fakeDockerHoldEnv: the signal that
	// ended it, empty when it was released.
	Signalled string `json:"signalled,omitempty"`
}

// fakeDocker answers each verb the way the CLI does, as much as --dockerrun
// reads of it.
func fakeDocker(log string, args []string) int {
	call := fakeCall{Args: args}
	code := 0
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch {
	case verb == "version":
		fmt.Println("arm64")
	case verb == "top":
		// The sidecar's command, which is plug's own definition of ready.
		fmt.Println("PID USER CMD\n1 root /opt/plug/bin/plug-linux-arm64 " + sidecarHoldVerb)
	case verb == "inspect":
		fmt.Println("true")
	case verb == "run" && len(args) == 2 && args[1] == "--help":
		fmt.Print(dockerRunHelpFixture)
	case verb == "run" && len(args) > 1 && args[1] == "-i":
		// The sidecar, attached: it lives until its stdin closes, which is the
		// whole of its tie to the plug that started it.
		call.Pgrp = processGroup()
		_, _ = io.Copy(io.Discard, os.Stdin)
		call.HoldEOF = true
	case verb == "run":
		call.Pgrp = processGroup()
		// The user's container. Read the resolver through the host side of its
		// -v, which is what the daemon does with it.
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-v" && strings.HasSuffix(args[i+1], ":/etc/resolv.conf:ro") {
				b, err := os.ReadFile(strings.TrimSuffix(args[i+1], ":/etc/resolv.conf:ro"))
				if err != nil {
					call.Read = err.Error()
				}
				call.Resolv = string(b)
			}
		}
		fmt.Sscan(os.Getenv(fakeDockerExitEnv), &code)
		if hold := os.Getenv(fakeDockerHoldEnv); hold != "" {
			if sig := fakeContainerRuns(hold); sig != "" {
				call.Signalled, code = sig, 143
			}
		}
	}
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake docker:", err)
		return 125
	}
	defer f.Close()
	b, _ := json.Marshal(call)
	_, _ = f.Write(append(b, '\n'))
	return code
}

// fakeContainerRuns is the user's container running: until the test releases
// it, or a SIGTERM ends it, which it reports.
func fakeContainerRuns(hold string) string {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	_ = os.WriteFile(hold+".started", nil, 0o600)
	deadline := time.After(20 * time.Second)
	for {
		select {
		case s := <-term:
			return s.String()
		case <-deadline:
			return "never released"
		case <-time.After(20 * time.Millisecond):
			if _, err := os.Stat(hold); err == nil {
				return ""
			}
		}
	}
}

// installFakeDocker puts the fake first in $PATH, under the name the OS looks
// docker up by, and returns the log its calls go to.
func installFakeDocker(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "docker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Unnarrowed, as every run of this test is: the narrowed $PATH of the
	// setuid helper has its own test (TestDockerIsResolvedInTheHumansPathNotTheNarrowedOne).
	saved := pathNarrowed
	pathNarrowed = false
	t.Cleanup(func() { pathNarrowed = saved })
	log := filepath.Join(t.TempDir(), "docker.log")
	t.Setenv(fakeDockerEnv, log)
	return log
}

func readFakeCalls(t *testing.T, log string) []fakeCall {
	t.Helper()
	f, err := os.Open(log)
	if err != nil {
		t.Fatalf("the fake docker was never called: %v", err)
	}
	defer f.Close()
	var calls []fakeCall
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var c fakeCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("a line of the fake's log is not JSON: %q", sc.Text())
		}
		calls = append(calls, c)
	}
	return calls
}

// argAfter returns the first value following flag in args that keep accepts.
func argAfter(args []string, flag string, keep func(string) bool) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && keep(args[i+1]) {
			return args[i+1]
		}
	}
	return ""
}

// assertHostPath checks the host side of a -v as the docker CLI of THIS OS
// reads it: an absolute native path, which on Windows starts with its drive.
// A relative or slash-converted path would be read by Docker Desktop as a
// named volume, and the container would get an empty directory in its place.
func assertHostPath(t *testing.T, what, host, want string) {
	t.Helper()
	if host != want {
		t.Errorf("%s is handed to docker as %q, want the file itself, %q", what, host, want)
	}
	if !filepath.IsAbs(host) {
		t.Errorf("%s is handed to docker as %q, which is not absolute: docker would take it for a volume name", what, host)
	}
	if runtime.GOOS == "windows" && filepath.VolumeName(host) == "" {
		t.Errorf("%s is handed to docker as %q, with no drive: Docker Desktop could not find it on this host", what, host)
	}
}

// The whole of a --dockerrun, sidecar to teardown, against the docker CLI as
// this OS finds it. The sidecar comes up with the profile key mounted from its
// native path, the user's container joins it and reads a resolver that exists
// on this host at the path given, the container's status is plug's, and
// everything plug made is gone once it returns.
func TestDockerRunDrivesTheDockerCLIOfThisOS(t *testing.T) {
	log := installFakeDocker(t)
	t.Setenv(fakeDockerExitEnv, "7")
	t.Setenv("PLUG_DOCKER_IMAGE", "softwarity/plug:test")
	t.Setenv("PLUG_DOCKER_NETWORK", "")
	saved := dockerMounts
	dockerMounts = nil
	t.Cleanup(func() { dockerMounts = saved })

	key := filepath.Join(t.TempDir(), "prod.key")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{host: "h.example", port: "2200", key: key}
	cfg.envPolicy.off = true   // no agent to read an environment from
	cfg.mountPolicy.off = true // nor volumes: the cifs path has its own test below

	got := runDockerRun(cfg, []string{"docker", "run", "--rm", "busybox", "true"}, nil, true)
	if got != 7 {
		t.Errorf("the container exited 7 and plug reported %d", got)
	}

	calls := readFakeCalls(t, log)
	var sidecar, user, removed []string
	var sidecarCall, userCall fakeCall
	for _, c := range calls {
		switch {
		case len(c.Args) == 2 && c.Args[0] == "run" && c.Args[1] == "--help":
			// The options the line is read with.
		case len(c.Args) > 1 && c.Args[0] == "run" && c.Args[1] == "-i":
			sidecar = c.Args
			sidecarCall = c
		case len(c.Args) > 0 && c.Args[0] == "run":
			user = c.Args
			userCall = c
			if c.Read != "" {
				t.Errorf("the resolver handed to the container could not be read from this host: %s", c.Read)
			}
			ns, search := tun.FirstInstanceResolver()
			if want := "nameserver " + ns + "\nsearch " + search + "\n"; c.Resolv != want {
				t.Errorf("the container's resolver reads %q, want %q", c.Resolv, want)
			}
		case len(c.Args) > 1 && c.Args[0] == "rm" && c.Args[1] == "-f":
			removed = c.Args
		}
	}
	if sidecar == nil || user == nil {
		t.Fatalf("docker was not asked for both containers; calls: %v", calls)
	}

	// The sidecar: this build's image, the daemon's architecture, the key from
	// its native path.
	name := argAfter(sidecar, "--name", func(string) bool { return true })
	joined := strings.Join(sidecar, " ")
	for _, want := range []string{"--entrypoint /opt/plug/bin/plug-linux-arm64 softwarity/plug:test", "-e PLUG_CORE_HOST=h.example", "-c"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sidecar is missing %q:\n  %s", want, joined)
		}
	}
	keySpec := argAfter(sidecar, "-v", func(v string) bool { return strings.HasSuffix(v, ":/plug/key:ro") })
	if keySpec == "" {
		t.Fatalf("the profile key is not mounted into the sidecar:\n  %s", joined)
	}
	assertHostPath(t, "the profile key", strings.TrimSuffix(keySpec, ":/plug/key:ro"), key)

	// The user's container: in the sidecar's network, its own line after ours.
	if joins := argAfter(user, "--network", func(string) bool { return true }); joins != "container:"+name {
		t.Errorf("the container joins %q, want the sidecar %q", joins, "container:"+name)
	}
	if len(user) < 3 || strings.Join(user[len(user)-3:], " ") != "--rm busybox true" {
		t.Errorf("the user's own line did not come last and whole: %q", user)
	}
	resolvSpec := argAfter(user, "-v", func(v string) bool { return strings.HasSuffix(v, ":/etc/resolv.conf:ro") })
	resolv := strings.TrimSuffix(resolvSpec, ":/etc/resolv.conf:ro")
	assertHostPath(t, "the resolver", resolv, filepath.Join(filepath.Dir(resolv), dockerRunResolvName))

	// The sidecar's tie to this process: its stdin, closed by the teardown
	// before the removal, and a process group of its own, so that the Ctrl-C
	// the user's container answers first does not take the sidecar down
	// before it has withdrawn its names. The user's container stays in ours.
	if !sidecarCall.HoldEOF {
		t.Error("the sidecar's stdin was never closed: it holds the session until something removes it")
	}
	if g := processGroup(); g != 0 {
		if sidecarCall.Pgrp == g {
			t.Error("the sidecar runs in plug's process group: the terminal's Ctrl-C and SIGHUP reach it")
		}
		if userCall.Pgrp != g {
			t.Errorf("the user's container left plug's process group (%d, want %d): a Ctrl-C would no longer reach it", userCall.Pgrp, g)
		}
	}

	// Teardown: the sidecar removed by its name, the session directory gone.
	if strings.Join(removed, " ") != "rm -f "+name {
		t.Errorf("the sidecar was not removed by its name after the run: last rm was %q", removed)
	}
	if _, err := os.Stat(resolv); !os.IsNotExist(err) {
		t.Errorf("the session directory outlived the run: %s (%v)", resolv, err)
	}
}

// A volume of the workload reaches a --dockerrun container as a cifs volume the
// daemon's kernel mounts through the forward this process holds, and the
// address it dials differs by OS: on Docker Desktop (macOS, Windows) the
// daemon runs in a VM and reaches this host as host.docker.internal; on Linux
// it runs here and dials the loopback. The wrong one is a volume that never
// mounts, after a helper started and a forward opened for nothing.
func TestTheContainersVolumeIsReachedAtThisOSsAddressForTheHost(t *testing.T) {
	log := installFakeDocker(t)
	saved := dockerMounts
	t.Cleanup(func() { dockerMounts = saved })
	dockerMounts = []dockerMount{{
		spec: mountSpec{name: "web", volume: "data", path: "/var/lib/data"},
		target: mountTarget{
			mountReply: mountReply{share: "vol", user: "plug"},
			pass:       "abcd",
			local:      "127.0.0.1:45678",
		},
	}}

	flags, cleanup := dockerMountFlags()
	cleanup()

	calls := readFakeCalls(t, log)
	if len(calls) != 2 || calls[0].Args[0] != "volume" || calls[0].Args[1] != "create" {
		t.Fatalf("want a volume create then its removal, got %v", calls)
	}
	create := calls[0].Args
	vol := create[len(create)-1]
	host := "127.0.0.1"
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		host = "host.docker.internal"
	}
	if dev := argAfter(create, "--opt", func(v string) bool { return strings.HasPrefix(v, "device=") }); dev != "device=//"+host+"/vol" {
		t.Errorf("the daemon is told to mount %q, want %q on %s", dev, "device=//"+host+"/vol", runtime.GOOS)
	}
	if o := argAfter(create, "--opt", func(v string) bool { return strings.HasPrefix(v, "o=") }); !strings.Contains(o, "port=45678") || !strings.Contains(o, "username=plug") {
		t.Errorf("the mount options do not name the forward's port and the account: %q", o)
	}
	if strings.Join(flags, " ") != "-v "+vol+":/var/lib/data" {
		t.Errorf("the container gets %q, want the volume at its cluster path", flags)
	}
	if strings.Join(calls[1].Args, " ") != "volume rm -f "+vol {
		t.Errorf("the volume was not removed after the run: %q", calls[1].Args)
	}
}

// The network's options on the user's line reach the sidecar, which owns that
// network, and not the container, where docker refuses them or, for
// --network, lets the last one win and takes the container out of the
// cluster without a word. The container still joins the sidecar, and gets
// the rest of its line as written.
func TestTheNetworksOptionsGoToTheSidecar(t *testing.T) {
	log := installFakeDocker(t)
	t.Setenv("PLUG_DOCKER_IMAGE", "softwarity/plug:test")
	t.Setenv("PLUG_DOCKER_NETWORK", "")
	saved := dockerMounts
	dockerMounts = nil
	t.Cleanup(func() { dockerMounts = saved })
	cfg := config{host: "h.example", port: "2200"}
	cfg.envPolicy.off = true
	cfg.mountPolicy.off = true

	if got := runDockerRun(cfg, []string{"docker", "run", "--rm", "-p", "3000:3000", "--network", "dev-net", "-h", "box",
		"-v", "/my/data:/var/lib/grafana", "grafana/grafana", "-p", "keep"}, nil, true); got != 0 {
		t.Fatalf("plug reported %d", got)
	}
	var sidecar, user []string
	for _, c := range readFakeCalls(t, log) {
		switch {
		case len(c.Args) > 1 && c.Args[0] == "run" && c.Args[1] == "-i":
			sidecar = c.Args
		case len(c.Args) > 1 && c.Args[0] == "run" && c.Args[1] != "--help":
			user = c.Args
		}
	}
	side := strings.Join(sidecar, " ")
	for _, want := range []string{"--publish=3000:3000", "--network=dev-net", "--hostname=box"} {
		if !strings.Contains(side, want) {
			t.Errorf("the sidecar is missing %q:\n  %s", want, side)
		}
	}
	if strings.Contains(side, "/var/lib/grafana") {
		t.Errorf("the user's mount went to the sidecar:\n  %s", side)
	}
	name := argAfter(sidecar, "--name", func(string) bool { return true })
	want := []string{"--network", "container:" + name}
	if len(user) < 4 || !reflect.DeepEqual(user[1:3], want) {
		t.Errorf("the container does not join the sidecar first: %q", user)
	}
	if tail := strings.Join(user[len(user)-6:], " "); tail != "--rm -v /my/data:/var/lib/grafana grafana/grafana -p keep" {
		t.Errorf("the container's own line is %q, want its options without the network's, then the image and its arguments", tail)
	}
	if n := strings.Count(strings.Join(user, " "), "--network"); n != 1 {
		t.Errorf("the container is given --network %d times: %q", n, user)
	}
}

// PLUG_DOCKER_NETWORK was the one way to put the sidecar on a network; the
// line's --network is now. Set, it is refused with the line to write, rather
// than read alongside or quietly ignored.
func TestPLUGDOCKERNETWORKSaysWhereItWent(t *testing.T) {
	log := installFakeDocker(t)
	t.Setenv("PLUG_DOCKER_NETWORK", "dev-net")
	if got := runDockerRun(config{host: "h", port: "22"}, []string{"docker", "run", "img"}, nil, true); got == 0 {
		t.Fatal("a session started with PLUG_DOCKER_NETWORK set")
	}
	if _, err := os.Stat(log); err == nil {
		t.Errorf("docker was called before the refusal: %v", readFakeCalls(t, log))
	}
}
