package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// `plug --dockerrun docker run <image>` puts a CONTAINER in the cluster, which
// prefixing docker with plug cannot do.
//
// WHY IT CANNOT. plug carries the traffic of the process it launches. Launched
// on `docker run`, the process it gets is the docker CLI, which posts a request
// to a socket and exits; the container is created by the docker daemon, which is
// nobody's child. Everything plug intercepted was that API call. The failure is
// silent, which is the worst part: plug says the tunnel is ready, the container
// runs, and nothing reaches the cluster.
//
// WHAT THIS DOES INSTEAD. It puts plug in the network namespace the container
// will use, rather than trying to drag the container into plug's. A sidecar
// container holds the datapath, and the user's container joins its namespace with
// --network container:. It is cross-platform for a reason worth stating: a
// container is a Linux environment on macOS and Windows too, so the Linux
// datapath is what runs in all three cases, VM or no VM.
//
// NO PARSING OF DOCKER'S GRAMMAR, deliberately. docker accepts its options in any
// order before the image name, so the two flags go in right after `run` and the
// user's line is passed through untouched. Working out where their options end
// and the image begins would mean knowing which of docker's sixty-odd flags take
// a value, a table that would rot. When the user's line conflicts with ours,
// docker refuses it and says so; explainDockerRefusal turns that into plug's
// terms rather than pre-empting it.
//
// The host needs no privilege here. Nothing creates a TUN on this machine: the
// capabilities are granted by docker, inside the sidecar.

// dockerRunResolv is what the user's container gets as its resolver. It is a
// mounted FILE rather than --dns, which docker refuses outright in this network
// mode ("conflicting options: dns and the network mode").
const dockerRunResolvName = "resolv.conf"

// dockerSidecarImage is where the Linux plug binary comes from. The published
// image carries one per architecture, already signed. Overridable because a
// build from source is stamped with a version that was never pushed anywhere,
// and telling someone to publish an image before they can try a flag would be
// absurd.
//
// It follows the FLAVOUR without being told, and that is worth knowing rather
// than rediscovering: the flavour rides in the version string, so a hosted
// launcher stamped 2.13.2-hosted composes softwarity/plug:2.13.2-hosted, which
// is exactly the tag its own release publishes. The property holds by
// composition, not by design, which is why there is a test on it: a sidecar
// running the standalone client for a gateway-served cluster would authenticate
// with the wrong identity model and fail somewhere far from here.
func dockerSidecarImage() string {
	if img := os.Getenv("PLUG_DOCKER_IMAGE"); img != "" {
		return img
	}
	return "softwarity/plug:" + version
}

// dockerRunCmd splices plug's two flags into the user's command line.
//
// Separated from everything that runs a process so the shape can be tested
// against a table instead of against a docker daemon. It refuses anything but
// `docker run`: --dockerrun is scoped to that one form on purpose, and a refusal
// naming what it accepts beats silently doing nothing to `docker compose up`.
func dockerRunCmd(cmdArgs []string, sidecar, resolv string) ([]string, error) {
	if len(cmdArgs) < 2 || cmdArgs[0] != "docker" || cmdArgs[1] != "run" {
		return nil, fmt.Errorf("--dockerrun runs `docker run`, and got %q.\n"+
			"      It is scoped to that one form: `docker compose up`, `docker create` and\n"+
			"      podman build their containers differently, and guessing at them would put\n"+
			"      a container in a cluster it was never asked to join",
			strings.Join(cmdArgs, " "))
	}
	out := []string{"docker", "run",
		"--network", "container:" + sidecar,
		"-v", resolv + ":/etc/resolv.conf:ro",
	}
	return append(out, cmdArgs[2:]...), nil
}

// explainDockerRefusal turns docker's own complaint into the sentence plug owes
// the user. Docker validates our injection against their line, which is why this
// exists instead of a parser: the two flags that can collide are the two docker
// itself names.
func explainDockerRefusal(stderr string) string {
	switch {
	case strings.Contains(stderr, "conflicting options: dns"):
		return "your `docker run` sets --dns, and the container takes its resolver from the cluster instead.\n" +
			"      Drop --dns: names resolve through plug, and anything else through the resolver it captured."
	case strings.Contains(stderr, "conflicting options") && strings.Contains(stderr, "network"):
		return "your `docker run` sets its own --network or publishes a port with -p, and the container\n" +
			"      shares the network of the sidecar that holds the tunnel, so neither can be set on it.\n" +
			"      A published port belongs on the sidecar; --dockerrun does not move it there yet."
	}
	return ""
}

// dockerServerArch is the architecture of the daemon's containers, which is not
// necessarily this machine's: it decides which of the image's Linux binaries the
// sidecar runs.
func dockerServerArch() (string, error) {
	out, err := exec.Command("docker", "version", "-f", "{{.Server.Arch}}").Output()
	if err != nil {
		return "", fmt.Errorf("asking docker for its architecture: %w "+
			"(is the docker daemon running?)", err)
	}
	arch := strings.TrimSpace(string(out))
	if arch == "" {
		return "", fmt.Errorf("docker reported no architecture")
	}
	return arch, nil
}

// startDockerSidecar brings up the container that holds the datapath and returns
// its name plus a teardown.
//
// The core is run DIRECTLY, PLUG_CORE=1 with the host, port and key in the
// environment: inside a throwaway container there is no profile to read and no
// wizard anyone could answer. The key is mounted read-only from the host, which
// is the only piece of the user's identity that crosses.
//
// Readiness is not scraped from a log. plug runs the command it was given only
// once the tunnel is up, so the command is what reports it: it touches a file,
// and the file appearing is plug's own definition of ready.
func startDockerSidecar(cfg config, network string, plugFlags []string) (string, func(), error) {
	arch, err := dockerServerArch()
	if err != nil {
		return "", nil, err
	}
	name := "plug-net-" + tun.ClusterHash(cfg.host+":"+cfg.port)

	// A leftover from a killed run would make `docker run --name` fail with a
	// name clash, which says nothing about plug. Ours to clean up, by name.
	_ = exec.Command("docker", "rm", "-f", name).Run()

	args := sidecarArgs(cfg, network, plugFlags, arch, name, dockerSidecarImage())

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("starting the sidecar that holds the tunnel: %v: %s\n"+
			"      Its image is %s; set PLUG_DOCKER_IMAGE to point at one that exists if this\n"+
			"      build's version was never published",
			err, strings.TrimSpace(string(out)), dockerSidecarImage())
	}
	stop := func() { _ = exec.Command("docker", "rm", "-f", name).Run() }

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("docker", "exec", name, "test", "-f", "/tmp/plug-ready").Run() == nil {
			return name, stop, nil
		}
		if exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).Run() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
	stop()
	return "", nil, fmt.Errorf("the sidecar never reported a tunnel to %s:%s. It said:\n%s",
		cfg.host, cfg.port, strings.TrimSpace(string(logs)))
}

// sidecarArgs builds the `docker run` for the container that holds the tunnel.
//
// Separated from the running of it so the shape can be asserted without a docker
// daemon, and because one branch of it had never executed anywhere: the profile
// KEY. A standalone cluster checks no personal key (flavour.go keeps keygen to
// the hosted build), so cfg.key is empty on every cluster this repository can
// stand up, and the mount below was written, compiled, shipped and never run.
// A test on the argv is not the same as a session authenticating with that key,
// and it does not pretend to be; it is the half that lives here. The other half
// is the gateway's, and belongs to the gateway's tests.
func sidecarArgs(cfg config, network string, plugFlags []string, arch, name, image string) []string {
	args := []string{"run", "-d", "--name", name,
		"--cap-add", "NET_ADMIN", // the TUN device and its routes
		"--cap-add", "SYS_ADMIN", // the per-launch mount namespace
		// The host's AppArmor profile blocks that mount namespace bind on Linux
		// hosts. e2e/compose.yml carries the same line for the same reason.
		"--security-opt", "apparmor:unconfined",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-e", "PLUG_CORE=1",
		"-e", "PLUG_CORE_HOST=" + cfg.host,
		"-e", "PLUG_CORE_PORT=" + cfg.port,
	}
	if network != "" {
		args = append(args, "--network", network)
	}
	// The profile's private key, read-only and by path. It is the ONE piece of
	// the caller's identity that crosses into the container, and it has to: the
	// core in there opens the tunnel, and a key that stops on this side is a key
	// never offered - the agent would see the built-in one and refuse a
	// developer their gateway had enrolled.
	if cfg.key != "" {
		args = append(args, "-v", cfg.key+":/plug/key:ro", "-e", "PLUG_CORE_KEY=/plug/key")
	}
	args = append(args, "--entrypoint", "/opt/plug/bin/plug-linux-"+arch, image)
	// -s and -c are the user's, forwarded rather than swallowed. A container in a
	// cluster is a member of it exactly as a process is, so the rule that a member
	// either has a name or declares itself a pure client holds here too. -s costs
	// nothing extra to honour: the user's container shares this one's network, so
	// a port it listens on is already on this one's loopback.
	args = append(args, plugFlags...)
	return append(args, "sh", "-c", "touch /tmp/plug-ready; sleep infinity")
}

// runDockerRun is the whole mode: hold the datapath in a sidecar, run the user's
// container in its network namespace, tear the sidecar down after.
//
// The sidecar's life is tied to this command's, which is the honest behaviour for
// a foreground run and a trap for `docker run -d`: a detached container would
// outlive the network it was given. Named rather than silently handled, because
// the two-container recipe is there for anyone who needs the container to
// survive, and half-supporting it would be worse than saying so.
func runDockerRun(cfg config, cmdArgs []string, exposes []string, client bool) int {
	dir, err := os.MkdirTemp("", "plug-docker")
	if err != nil {
		info("cannot write the resolver the container will read: %v", err)
		return 1
	}
	defer os.RemoveAll(dir)
	resolv := filepath.Join(dir, dockerRunResolvName)
	// 0644 and a world-readable directory: the docker daemon reads this file as
	// root, and on macOS it crosses into a VM. Nothing secret is in it.
	if err := os.Chmod(dir, 0o755); err != nil {
		info("cannot make the resolver readable by the docker daemon: %v", err)
		return 1
	}
	ns, search := tun.FirstInstanceResolver()
	if err := os.WriteFile(resolv, []byte("nameserver "+ns+"\nsearch "+search+"\n"), 0o644); err != nil {
		info("cannot write the resolver the container will read: %v", err)
		return 1
	}

	var plugFlags []string
	for _, e := range exposes {
		plugFlags = append(plugFlags, "-s", e)
	}
	if client {
		plugFlags = append(plugFlags, "-c")
	}

	network := os.Getenv("PLUG_DOCKER_NETWORK")
	name, stop, err := startDockerSidecar(cfg, network, plugFlags)
	if err != nil {
		info("%v", err)
		return 1
	}
	defer stop()

	full, err := dockerRunCmd(cmdArgs, name, resolv)
	if err != nil {
		info("%v", err)
		return 1
	}
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	var errBuf strings.Builder
	cmd.Stderr = &teeWriter{to: os.Stderr, into: &errBuf}
	err = cmd.Run()
	if err != nil {
		if why := explainDockerRefusal(errBuf.String()); why != "" {
			info("%s", why)
		}
	}
	return exitCodeOf(err)
}

// teeWriter passes the child's stderr through untouched and keeps a copy, so a
// docker refusal can be explained without swallowing what docker said.
type teeWriter struct {
	to   *os.File
	into *strings.Builder
}

func (w *teeWriter) Write(p []byte) (int, error) {
	w.into.Write(p)
	return w.to.Write(p)
}

// exitCodeOf reports the child's exit status, so `plug --dockerrun docker run …`
// exits as the container did.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
