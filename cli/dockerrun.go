package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

// dockerRunEnvName is the workload's environment for the user's container, as
// an --env-file in the same session directory: 0600, unlike the resolver, and
// gone with the directory.
const dockerRunEnvName = "env"

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
func dockerRunCmd(cmdArgs []string, sidecar, resolv string, projected []string) ([]string, error) {
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
	// The workload's environment and mounted files, as -e and -v. They go BEFORE
	// the user's own args so that a -e or -v the user writes wins - docker takes
	// the last of a repeated flag, the same "caller wins" the -s/-c projection
	// gives by skipping keys the shell already set.
	out = append(out, projected...)
	return append(out, cmdArgs[2:]...), nil
}

// dockerProjectionFlags turns a workload's projected environment and mounted
// files into `docker run` flags. The environment goes through a FILE and
// --env-file, never through -e: the workload's variables are its secrets, and
// an argv is public for the life of the container (ps, /proc, the process
// table of every account on the machine). The file is 0600 in the session's
// temp directory, written once, removed with it. Each mounted file path becomes
// a -v of the local copy onto its EXACT cluster path: a --dockerrun container
// is Linux and expects /certificates where the pod had it, so nothing is
// repointed here, unlike the local-process case that cannot write the host's
// real paths.
//
// The env-file format is docker's: one KEY=VALUE per line, the value taken
// verbatim (quotes included, no escapes), so a value holding a newline cannot
// be written as a line. Those go the one other way docker offers: a bare KEY
// line makes the docker CLI read the variable from ITS OWN environment, and
// the second return value is what to put there. Still no argv involved.
//
// Keys are sorted so the file and the flags are stable and testable.
func dockerProjectionFlags(set map[string]string, filesDir string, paths []string, envFile string) (flags, env []string, err error) {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		var body strings.Builder
		for _, k := range keys {
			v := set[k]
			if strings.ContainsAny(v, "\n\r") {
				body.WriteString(k + "\n")
				env = append(env, k+"="+v)
				continue
			}
			body.WriteString(k + "=" + v + "\n")
		}
		// O_EXCL, so a file somebody else planted at that name is a failure and
		// not a write through it; 0600 from the first byte, and the file handed
		// to the user where plug holds euid 0.
		f, err := os.OpenFile(envFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("writing the container's environment file: %w", err)
		}
		if _, err := f.WriteString(body.String()); err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("writing the container's environment file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, nil, err
		}
		chownToUser(envFile)
		flags = append(flags, "--env-file", envFile)
	}
	for _, p := range paths {
		flags = append(flags, "-v", filepath.Join(filesDir, p)+":"+p+":ro")
	}
	return flags, env, nil
}

// dockerProjection fetches the workload's environment and mounted files from the
// agent and turns them into `docker run` flags for the user's container, the
// variables the docker CLI itself must carry (see dockerProjectionFlags), and a
// cleanup for the files temp and the env file (called once the container has
// exited). sessionDir is where the env file goes. The source is --env-of when
// given, else the single -s name; nothing when projection is off, or when
// there is no one name to read from.
func dockerProjection(cfg config, exposes []string, sessionDir string) (flags, env []string, cleanup func()) {
	noop := func() {}
	src := dockerEnvSource(cfg, exposes)
	if src == "" {
		return nil, nil, noop
	}
	tr, err := dialTunnel(cfg)
	if err != nil {
		info("could not reach the agent to project %s's environment (%v); the container starts without it", src, err)
		return nil, nil, noop
	}
	defer tr.Close()
	reply, err := readWorkloadEnv(tr, src)
	if err != nil {
		info("%s: could not read the workload's environment (%v); the container starts without it", src, err)
		return nil, nil, noop
	}
	if reply.agentErr != "" {
		info("%s: the agent did not hand over the environment: %s", src, reply.agentErr)
		return nil, nil, noop
	}
	for _, n := range reply.notes {
		info("%s: %s", src, n)
	}
	// No caller env to merge against - a container inherits none of this host's
	// shell - so only the policy's drops apply; a -e the user writes wins
	// because docker reads the env files first and the -e flags after them.
	set, _, _ := mergeWorkloadEnvWithEmpty(reply.vars, nil, cfg.envPolicy)
	dir, paths, ferr := fetchWorkloadFiles(tr, src)
	if ferr != nil {
		info("%s: could not read the workload's mounted files (%v); the container starts without them", src, ferr)
	}
	envFile := filepath.Join(sessionDir, dockerRunEnvName)
	cleanup = func() {
		_ = os.Remove(envFile)
		if dir != "" {
			os.RemoveAll(dir)
		}
	}
	flags, env, err = dockerProjectionFlags(set, dir, paths, envFile)
	if err != nil {
		info("%s: %v; the container starts without the workload's environment", src, err)
		cleanup()
		return nil, nil, noop
	}
	if len(flags) > 0 {
		info("%s: projected %d variable(s) and %d mounted path(s) into the container", src, len(set), len(paths))
	}
	return flags, env, cleanup
}

// dockerEnvSource is the workload whose environment --dockerrun projects: the
// --env-of name if given, otherwise the single -s name. Nothing when projection
// is off or when there is no one name to point at.
func dockerEnvSource(cfg config, exposes []string) string {
	if cfg.envPolicy.off {
		return ""
	}
	if cfg.envPolicy.from != "" {
		return cfg.envPolicy.from
	}
	if len(exposes) == 1 {
		if spec, err := parseExpose(exposes[0]); err == nil {
			return spec.Name
		}
	}
	return ""
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

// dockerCommand is the one way this file runs the docker CLI: as the HUMAN, with
// the human's $PATH, exactly as runChildEnv runs the command of a plain session.
//
// The launcher that gets here is the setuid-root helper on macOS, and it has
// narrowed its own $PATH to the system directories (securePath). Both halves of
// that are wrong for docker. The CLI is not a system helper: Docker Desktop puts
// it in /usr/local/bin, Homebrew elsewhere again, and neither is in the narrowed
// list, so a bare exec.Command("docker") found nothing. And docker is the user's
// tool, driven by the user's ~/.docker: the current context, the credential
// store, the credential helpers it spawns by name from $PATH. Run as root, it
// read root's config (no context, no login) and left root-owned files behind in
// the user's home when it wrote any. So: resolve docker in the $PATH the human
// had, hand it that $PATH back, and drop the credentials to the human
// (applyPrivDrop). The daemon needs none of this process's privilege: the TUN,
// the mount namespace and the cifs mounts all happen on the daemon's side, and
// every file this host hands docker is chowned to the user first.
//
// Where nothing was narrowed (Linux capabilities, Windows, an unprivileged run)
// every step is a no-op and the command is exactly what exec.Command gave.
func dockerCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	if pathNarrowed {
		if p, err := lookPathIn("docker", userPath); err == nil {
			cmd.Path = p
			// exec.Command already failed to find docker in the narrowed $PATH and
			// parked that error in cmd.Err, which Start would return even with
			// Path corrected. This resolution supersedes it (same as runChildEnv).
			cmd.Err = nil
		}
		cmd.Env = withUserPath(nil)
	}
	applyPrivDrop(cmd)
	return cmd
}

// dockerServerArch is the architecture of the daemon's containers, which is not
// necessarily this machine's: it decides which of the image's Linux binaries the
// sidecar runs.
func dockerServerArch() (string, error) {
	out, err := dockerCommand("version", "-f", "{{.Server.Arch}}").Output()
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
	owner := thisSidecarOwner(cfg)
	name := owner.name()

	// A leftover from a killed run would make `docker run --name` fail with a
	// name clash, which says nothing about plug. Ours to clean up, but only OURS:
	// the name used to be the cluster hash alone and the cleanup a `docker rm -f`
	// of it, so a second --dockerrun towards the same cluster removed the first
	// one's sidecar, which was that session's whole network, without a word to
	// either of them. The name now carries the owner's PID and the labels say
	// whose it is; only a sidecar whose owner is gone is a leftover.
	removeStaleSidecars(owner)

	args := sidecarArgs(cfg, network, plugFlags, arch, name, dockerSidecarImage(), owner)

	out, err := dockerCommand(args...).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("starting the sidecar that holds the tunnel: %v: %s\n"+
			"      Its image is %s; set PLUG_DOCKER_IMAGE to point at one that exists if this\n"+
			"      build's version was never published",
			err, strings.TrimSpace(string(out)), dockerSidecarImage())
	}
	stop := func() { _ = dockerCommand("rm", "-f", name).Run() }

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if dockerCommand("exec", name, "test", "-f", "/tmp/plug-ready").Run() == nil {
			return name, stop, nil
		}
		if dockerCommand("inspect", "-f", "{{.State.Running}}", name).Run() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := dockerCommand("logs", "--tail", "20", name).CombinedOutput()
	stop()
	return "", nil, fmt.Errorf("the sidecar never reported a tunnel to %s:%s. It said:\n%s",
		cfg.host, cfg.port, strings.TrimSpace(string(logs)))
}

// The labels a sidecar carries, so that whose it is can be asked of the docker
// daemon rather than guessed from a name. plug.cluster says which agent it holds
// a tunnel to; plug.owner.pid and plug.owner.host say which plug process, on
// which machine, is running on top of it. The machine matters because a daemon
// can be shared (DOCKER_HOST): a PID is only meaningful on the host that owns it.
const (
	sidecarClusterLabel = "plug.cluster"
	sidecarOwnerLabel   = "plug.owner.pid"
	sidecarHostLabel    = "plug.owner.host"
)

// sidecarOwner identifies the plug session a sidecar belongs to: the cluster it
// holds a tunnel to (tun.ClusterHash of host:port) and the process, on this
// machine, that runs on top of it.
type sidecarOwner struct {
	cluster string
	pid     int
	host    string // os.Hostname(), empty when the OS could not say
}

func thisSidecarOwner(cfg config) sidecarOwner {
	host, _ := os.Hostname()
	return sidecarOwner{cluster: tun.ClusterHash(cfg.host + ":" + cfg.port), pid: os.Getpid(), host: host}
}

// name is the sidecar's container name, which the user's container joins with
// --network container:<name>. It carries the PID so two sessions towards the
// same cluster, on the same machine, never share one: the cluster hash alone
// made the second `docker run --name` clash with the first, and the cleanup
// that avoided the clash removed a live session's network.
func (o sidecarOwner) name() string {
	return "plug-net-" + o.cluster + "-" + strconv.Itoa(o.pid)
}

// labels are the --label flags that make the owner readable back from docker.
func (o sidecarOwner) labels() []string {
	return []string{
		"--label", sidecarClusterLabel + "=" + o.cluster,
		"--label", sidecarOwnerLabel + "=" + strconv.Itoa(o.pid),
		"--label", sidecarHostLabel + "=" + o.host,
	}
}

// filters select, among everything the daemon has, the sidecars this process
// may judge: same cluster, started from this machine.
func (o sidecarOwner) filters() []string {
	return []string{
		"--filter", "label=" + sidecarClusterLabel + "=" + o.cluster,
		"--filter", "label=" + sidecarHostLabel + "=" + o.host,
	}
}

// removeStaleSidecars removes the sidecars of this cluster, from this machine,
// whose owning process is gone. It asks docker for name and owner PID in one
// listing and decides with staleSidecars; a daemon that cannot be listed leaves
// everything in place, and the `docker run` that follows says what is wrong.
func removeStaleSidecars(owner sidecarOwner) {
	args := append([]string{"ps", "-a", "--format", "{{.Names}}\t{{.Label \"" + sidecarOwnerLabel + "\"}}"}, owner.filters()...)
	out, err := dockerCommand(args...).Output()
	if err != nil {
		return
	}
	for _, name := range staleSidecars(string(out), owner.pid, processAlive) {
		_ = dockerCommand("rm", "-f", name).Run()
	}
}

// staleSidecars reads a `docker ps` listing of "name<TAB>owner pid" lines and
// names the sidecars whose owner is dead. Separated from docker so the decision
// can be tested against a table, since the cost of getting it wrong is another
// session's network.
//
// A sidecar labelled with THIS process's PID is stale too: this process has
// just started and owns nothing yet, so the label is a previous life of the PID
// (the OS reuses them), and the name it carries is the one about to be taken.
// A line whose PID does not parse is nobody's to judge and is left alone.
func staleSidecars(listing string, self int, alive func(int) bool) []string {
	var stale []string
	for _, line := range strings.Split(listing, "\n") {
		name, pidText, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil || pid <= 0 {
			continue
		}
		if pid == self || !alive(pid) {
			stale = append(stale, name)
		}
	}
	return stale
}

// sidecarArgs builds the `docker run` for the container that holds the tunnel.
//
// Separated from the running of it so the shape can be asserted without a docker
// daemon, and because one branch of it had never executed anywhere: the profile
// KEY. A standalone cluster checks no personal key (flavour.go keeps keygen to
// the hosted build), so cfg.key was empty on every cluster this repository knew
// how to stand up, and the mount below was written, compiled, shipped and never
// run.
//
// A test on the argv is not a session authenticating with that key, so the e2e
// now builds the cluster that was missing rather than waiting for one: the
// keymount cell in scripts/ci/e2e-matrix.sh runs an agent whose authorized_keys
// holds one throwaway key and NOT the key built into plug. The built-in key is
// refused there, so a tunnel that comes up could only have been carried by the
// file mounted below, and the same profile with its key line removed is refused.
func sidecarArgs(cfg config, network string, plugFlags []string, arch, name, image string, owner sidecarOwner) []string {
	args := []string{"run", "-d", "--name", name}
	args = append(args, owner.labels()...) // whose it is, for the next session's cleanup to read
	args = append(args,
		"--cap-add", "NET_ADMIN", // the TUN device and its routes
		"--cap-add", "SYS_ADMIN", // the per-launch mount namespace
		// The host's AppArmor profile blocks that mount namespace bind on Linux
		// hosts. e2e/compose.yml carries the same line for the same reason.
		"--security-opt", "apparmor:unconfined",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-e", "PLUG_CORE=1",
		"-e", "PLUG_CORE_HOST="+cfg.host,
		"-e", "PLUG_CORE_PORT="+cfg.port,
	)
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

	// The workload's environment and mounted files, projected into the user's
	// container as --env-file and -v. Built on THIS host: the sidecar holds only
	// the datapath, so its own env never reaches the container. The files temp
	// and the env file live until the container exits, which is when this
	// function returns.
	projected, cliEnv, cleanup := dockerProjection(cfg, exposes, dir)
	defer cleanup()
	// Its volumes too, mounted on THIS host as for a process (startMounts)
	// and handed to the container at their exact cluster path with -v: the
	// container is Linux and expects the real path, so no repointing here.
	stopMounts, err := startMounts(dockerMountConfig(cfg, exposes))
	if err != nil {
		info("mount: %v", err)
		return 1
	}
	defer stopMounts()
	volFlags, volCleanup := dockerMountFlags()
	defer volCleanup()
	projected = append(projected, volFlags...)

	full, err := dockerRunCmd(cmdArgs, name, resolv, projected)
	if err != nil {
		info("%v", err)
		return 1
	}
	// full[0] is "docker": dockerRunCmd refused anything else above. The user's
	// container is started by the CLI running as the user, with the user's
	// $PATH and ~/.docker, like every other docker call here (dockerCommand).
	cmd := dockerCommand(full[1:]...)
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	// The multi-line values, carried by the docker CLI's own environment and
	// named in the env file by a bare KEY line (dockerProjectionFlags). Added
	// to the environment dockerCommand chose, which is nil when nothing was
	// narrowed (inherit) and the human's $PATH restored otherwise.
	if len(cliEnv) > 0 {
		env := cmd.Env
		if env == nil {
			env = os.Environ()
		}
		cmd.Env = append(env, cliEnv...)
	}
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

// dockerMountConfig is the config startMounts reads for a --dockerrun: the
// automatic mounts belong to the workload whose environment the container
// gets (dockerEnvSource), which the raw -s values do not name in cfg.exposes.
func dockerMountConfig(cfg config, exposes []string) config {
	if src := dockerEnvSource(cfg, exposes); src != "" && cfg.envPolicy.from == "" && len(cfg.exposes) == 0 {
		cfg.envPolicy.from = src
	}
	return cfg
}

// dockerMountFlags turns what startMounts collected into docker volumes of
// type cifs - one per mount, created now, mounted by the daemon's kernel when
// the container starts, through the forward this process keeps open - and
// the -v flags that put each at its cluster path in the container. The
// cleanup removes the volumes once the container has exited. Read-write:
// that is the point.
func dockerMountFlags() ([]string, func()) {
	var flags, made []string
	for _, m := range dockerMounts {
		host, port, err := net.SplitHostPort(m.target.local)
		if err != nil {
			info("mount %s: %v", m.spec, err)
			continue
		}
		_ = host // the forward is on this host's loopback; the daemon reaches it as dockerHostAddr
		name := "plug-vol-" + recordName(m.spec.name + ":" + m.spec.volume + ":" + port)[:16]
		opts := fmt.Sprintf("port=%s,username=%s,password=%s,vers=3.0,uid=0,gid=0,file_mode=0664,dir_mode=0775,noperm,nobrl",
			port, m.target.user, m.target.pass)
		// As the user, like every docker call here: the cifs mount itself is the
		// daemon's kernel's work, so the volume needs nothing of this process's
		// privilege, and the user must be able to `docker volume rm` a leftover.
		cmd := dockerCommand("volume", "create", "--driver", "local",
			"--opt", "type=cifs", "--opt", "device=//"+dockerHostAddr()+"/"+m.target.share, "--opt", "o="+opts, name)
		if out, err := cmd.CombinedOutput(); err != nil {
			info("mount %s: creating the cifs volume: %s", m.spec, strings.TrimSpace(string(out)))
			continue
		}
		made = append(made, name)
		in := m.spec.path
		if in == "" {
			in = m.spec.volume
		}
		flags = append(flags, "-v", name+":"+in)
		info("mount %s: the container gets it at %s (cifs volume %s)", m.spec.volume, in, name)
	}
	return flags, func() {
		for _, name := range made {
			_ = dockerCommand("volume", "rm", "-f", name).Run()
		}
	}
}
