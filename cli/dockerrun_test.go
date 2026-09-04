package main

import (
	"os"
	"strings"
	"testing"
)

// The splice is the whole of --dockerrun's contract with docker, and it is a
// contract precisely BECAUSE it does not parse. Our two flags go in right after
// `run`, where docker accepts options in any order, and everything the user wrote
// follows untouched. Anything that reordered, dropped or reformatted their line
// would be plug editing a command it does not understand.
func TestDockerRunCmdSplicesWithoutTouchingTheUsersLine(t *testing.T) {
	user := []string{"docker", "run", "--rm", "-e", "A=1", "my-image", "sh", "-c", "echo run --network hi"}
	got, err := dockerRunCmd(user, "plug-net-abc", "/tmp/x/resolv.conf")
	if err != nil {
		t.Fatalf("a plain `docker run` was refused: %v", err)
	}
	want := []string{
		"docker", "run",
		"--network", "container:plug-net-abc",
		"-v", "/tmp/x/resolv.conf:/etc/resolv.conf:ro",
		"--rm", "-e", "A=1", "my-image", "sh", "-c", "echo run --network hi",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d:\n got %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d is %q, want %q (whole line: %q)", i, got[i], want[i], got)
		}
	}
}

// The user's own words can contain anything, including the words we splice. The
// case above ends with `echo run --network hi` for that reason: a splice that
// searched for "run" or "--network" instead of taking the second position would
// pass the table above and corrupt this line.
func TestDockerRunCmdRefusesEverythingItCannotHonour(t *testing.T) {
	for _, c := range []struct {
		why  string
		args []string
	}{
		{"compose builds its containers from a file, not from this line", []string{"docker", "compose", "up"}},
		{"create then start is two commands, and the network is set on the first", []string{"docker", "create", "my-image"}},
		{"another engine entirely", []string{"podman", "run", "my-image"}},
		{"docker with no verb", []string{"docker"}},
		{"nothing at all", nil},
		{"a bare command, which is what plug runs WITHOUT this flag", []string{"npm", "run", "dev"}},
	} {
		if _, err := dockerRunCmd(c.args, "side", "/tmp/r"); err == nil {
			t.Errorf("%q was accepted; it must be refused: %s", c.args, c.why)
		} else if !strings.Contains(err.Error(), "docker run") {
			t.Errorf("the refusal for %q does not say what IS accepted: %v", c.args, err)
		}
	}
}

// docker validates the user's line against ours, which is why plug does not
// pre-empt it with a parser. The price is that the refusal arrives in docker's
// words, about flags the user never typed. These translate it; a message that no
// longer matches means the translation silently stops happening, and the user is
// left with "conflicting options" and no idea which options.
func TestDockerRefusalsAreTranslated(t *testing.T) {
	for _, c := range []struct{ what, stderr, mustSay string }{
		{"a --dns of their own",
			"docker: Error response from daemon: conflicting options: dns and the network mode",
			"--dns"},
		{"a --network of their own",
			"docker: Error response from daemon: conflicting options: container type network can't be used with links. This would result in undefined behavior",
			"--network"},
		{"a published port",
			"docker: Error response from daemon: conflicting options: port publishing and the container type network mode",
			"port"},
	} {
		if got := explainDockerRefusal(c.stderr); !strings.Contains(got, c.mustSay) {
			t.Errorf("%s: plug added nothing useful to docker's %q, said %q", c.what, c.stderr, got)
		}
	}
	if got := explainDockerRefusal("my-image: command not found"); got != "" {
		t.Errorf("a failure that is the container's own must not be explained as plug's: %q", got)
	}
}

// The default image is this build's version, which for anything but a release
// was never pushed anywhere. The override is what makes the flag testable at all
// before a release, so it must actually win.
func TestTheSidecarImageCanBeOverridden(t *testing.T) {
	t.Setenv("PLUG_DOCKER_IMAGE", "")
	if got := dockerSidecarImage(); !strings.HasSuffix(got, version) {
		t.Errorf("the default image is %q, which does not name this build (%s)", got, version)
	}
	t.Setenv("PLUG_DOCKER_IMAGE", "localhost:5000/plug:wip")
	if got := dockerSidecarImage(); got != "localhost:5000/plug:wip" {
		t.Errorf("PLUG_DOCKER_IMAGE was ignored: %q", got)
	}
	_ = os.Unsetenv("PLUG_DOCKER_IMAGE")
}

// The sidecar must run the client of the SAME flavour as the launcher starting
// it, and nothing in the code says so out loud: it works because the flavour is
// part of the version string and the image tag is built from that string. A
// hosted cluster checks a personal key that the standalone client does not even
// have a verb to create (see flavour.go), so getting this wrong would fail at
// authentication, one container away from anything that mentions flavours.
func TestTheSidecarFollowsTheLauncherFlavour(t *testing.T) {
	saved := version
	defer func() { version = saved }()
	t.Setenv("PLUG_DOCKER_IMAGE", "")

	for _, c := range []struct{ version, want string }{
		{"2.13.2", "softwarity/plug:2.13.2"},
		{"2.13.2-hosted", "softwarity/plug:2.13.2-hosted"},
		{"dev+abc1234-hosted", "softwarity/plug:dev+abc1234-hosted"},
	} {
		version = c.version
		if got := dockerSidecarImage(); got != c.want {
			t.Errorf("a launcher stamped %q would pull %q, want %q", c.version, got, c.want)
		}
	}
}
