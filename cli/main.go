package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

//go:embed keys/id_ed25519
var embeddedKey []byte

// version is stamped at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

const sshUser = "plug" // tunnel user (public-key)
const getUser = "get"  // download user (passwordless, ForceCommand)
const defaultPort = "2222"
const agentHome = "/opt/plug"

// usage lists the everyday commands only — no implementation talk, and no rarely
// needed ones (down: the background tears itself down — still works, just unlisted).
func usage() string {
	return `plug — run a local command as a member of your cluster.

Usage:
  plug [-p profile] -s <name>:<cluster-port>:<local-port> <command> [args...]
                                       run <command> as a named member of the
                                       cluster — it answers to <name>, and
                                       reaches cluster services by name in return
  plug ls                              list profiles
  plug test [profile]                  check an agent is reachable
  plug rn <old> <new>                  rename a profile (alias: mv)
  plug rm <profile>                    remove a profile
  plug versions                        list cached versions
  plug uninstall                       remove plug from this machine
  plug about                           what plug is, in a few lines

Options:
  -p, --profile <name>   use profile ~/.plug/<name>.conf
  -H, --host <host>      agent host
      --port <port>      agent SSH port (default 2222)
  -s, --serve <name>:<cluster-port>:<local-port>
                         publish this process in the cluster as <name>: workloads
                         reaching <name>:<cluster-port> land on 127.0.0.1:<local-port>
                         for this session. The agent creates the name on the fly
                         (Docker socket / Kubernetes RBAC), or you pre-declare it.
                         Repeatable; place after the other options.
  -h, --help             show this help
`
}

// cmdAbout explains the concept in a few lines — the "why", not the plumbing.
func cmdAbout() {
	fmt.Print(`plug runs your local command as a member of your cluster: cluster service names
resolve, its services are reachable, and your process is itself reachable in the
cluster under a name — no code change, no proxy config.

Set it up once per cluster (the install grants the privilege plug needs), then:

  plug -s my-app:8080:3000 npm run start:dev

Your process now reaches cluster services by name and answers at my-app:8080.
Several clusters? Just name one with -p — plug creates the profile on first run:

  plug -p staging -s my-app:8080:3000 npm run start:dev

Docs: https://softwarity.github.io/plug/
`)
}

type config struct {
	host     string
	port     string
	forwards []forwardSpec
	exposes  []tunnel.ExposeSpec
}

// parseExpose parses one -s value, <name>:<cluster-port>:<local-port> — the
// reverse direction (see tunnel/expose.go).
// exposeName is the RFC 1035 label both agent backends accept (leading letter,
// so the name is a valid Kubernetes Service too). Mirrored here so a bad -s name
// fails instantly, before connecting, with the same rule the agent enforces.
var exposeName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func parseExpose(s string) (tunnel.ExposeSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] == "" {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s wants <name>:<cluster-port>:<local-port>, got %q", s)
	}
	if !exposeName.MatchString(parts[0]) {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s %s: %q is not a valid name — a cluster DNS name is a "+
			"lowercase letter then letters, digits or hyphens (max 63), e.g. my-app", s, parts[0])
	}
	for _, p := range parts[1:] {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return tunnel.ExposeSpec{}, fmt.Errorf("-s %s: %q is not a valid port", s, p)
		}
	}
	return tunnel.ExposeSpec{Name: parts[0], ClusterPort: parts[1], LocalPort: parts[2]}, nil
}

// attachExposes parses the raw -s values for an in-process core run — the
// grammar here IS this binary's grammar, so failing now is legitimate. (The
// exec path forwards them raw instead: the downloaded core owns the grammar.)
func attachExposes(cfg *config, raw []string) {
	for _, r := range raw {
		spec, err := parseExpose(r)
		if err != nil {
			fatal("%v", err)
		}
		cfg.exposes = append(cfg.exposes, spec)
	}
}

// stripLeadingExposes pops the -s/--serve pairs a launcher left at the head of
// the core's argv (see launcherRun) and parses them — an old launcher forwards
// them there without understanding them.
func stripLeadingExposes(args []string) ([]tunnel.ExposeSpec, []string, error) {
	var specs []tunnel.ExposeSpec
	for len(args) >= 2 && (args[0] == "-s" || args[0] == "--serve") {
		spec, err := parseExpose(args[1])
		if err != nil {
			return nil, nil, err
		}
		specs = append(specs, spec)
		args = args[2:]
	}
	return specs, args, nil
}

// forwardSpec declares a local port-forward for a raw-TCP service whose driver
// ignores the SOCKS proxy. Declared in a profile as:
//
//	forward = AMQP_URL=amqp://rabbitmq:5672, MONGO_URL=mongodb://mongodb:27017
//
// plug opens a per-session local port to target and injects env=<local URL>.
type forwardSpec struct {
	env    string // env var to set for the child
	target string // cluster host:port to dial through the tunnel
	rawURL string // original URL (with scheme) if the value was one, else ""
}

func parseForward(s string) (forwardSpec, bool) {
	name, val, ok := strings.Cut(s, "=")
	if !ok {
		return forwardSpec{}, false
	}
	name, val = strings.TrimSpace(name), strings.TrimSpace(val)
	if name == "" || val == "" {
		return forwardSpec{}, false
	}
	if strings.Contains(val, "://") {
		u, err := url.Parse(val)
		if err != nil || u.Host == "" {
			return forwardSpec{}, false
		}
		return forwardSpec{env: name, target: u.Host, rawURL: val}, true
	}
	return forwardSpec{env: name, target: val}, true
}

// decl reconstructs the "NAME=VALUE" declaration, for passing across the
// launcher→core exec boundary.
func (f forwardSpec) decl() string {
	if f.rawURL != "" {
		return f.env + "=" + f.rawURL
	}
	return f.env + "=" + f.target
}

// localValue is the env value pointing the child at the local forward: the
// original URL with its host rewritten to localAddr, or just localAddr.
func (f forwardSpec) localValue(localAddr string) string {
	if f.rawURL != "" {
		if u, err := url.Parse(f.rawURL); err == nil {
			u.Host = localAddr
			return u.String()
		}
	}
	return localAddr
}

type options struct {
	profile string
	host    string
	port    string
	exposes []string // raw -s values; validated once, re-prefixed on the core exec
}

func main() {
	// Mount-namespace shim: a re-exec of ourselves (inside the child's new mount
	// ns) that bind-mounts its private resolv.conf and execs the real command.
	// Checked first — it inherits PLUG_CORE=1 and must not fall into coreMain.
	if len(os.Args) > 1 && os.Args[1] == tun.NsShimVerb {
		if err := tun.NsShimMain(os.Args[2:]); err != nil {
			fatal("%v", err)
		}
		return
	}
	// The persistent macOS datapath daemon: a detached re-exec that holds the
	// datapath for one cluster (see daemonMain). Checked before PLUG_CORE.
	if len(os.Args) > 1 && os.Args[1] == tun.DaemonVerb {
		os.Exit(daemonMain(os.Args[2:]))
	}
	// Core mode: this binary was exec'd by the launcher to do the real work.
	if os.Getenv("PLUG_CORE") == "1" {
		coreMain()
		return
	}

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage())
		os.Exit(2)
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage())
		return
	case "version":
		fmt.Println(version)
		return
	case "about":
		cmdAbout()
		return
	case "versions":
		listVersions()
		return
	case "init":
		initProfile()
		return
	case "ls":
		cmdListProfiles()
		return
	case "rm":
		cmdRemoveProfile(args[1:])
		return
	case "rn", "mv":
		cmdRenameProfile(args[1:])
		return
	case "test":
		cmdTestProfile(args[1:])
		return
	case "uninstall":
		uninstall(args[1:])
		return
	case "down":
		cmdDown(args[1:])
		return
	case "install-service":
		installService() // Windows: create the SCM datapath service (elevated, once)
		return
	case "remove-service":
		removeService()
		return
	case "selftest":
		os.Exit(runSelfTest())
	}
	launcherRun(args)
}

// serveRequired enforces the one invocation shape: a command joins the cluster
// AS a named member, so at least one VALID -s <name>:<cluster-port>:<local-port>
// is mandatory — even when nothing calls back (name it anyway; most of the time
// something will, and it keeps a single form to learn). Run before connecting,
// so a missing or malformed -s fails instantly. Subcommands never reach here:
// main() dispatches ls/test/about/… before launcherRun.
func serveRequired(exposes []string) error {
	if len(exposes) == 0 {
		return errors.New("name your process in the cluster:\n" +
			"  plug [-p profile] -s <name>:<cluster-port>:<local-port> <command> [args...]\n" +
			"-s is required: a running process in a cluster is a service, and a service has a name —\n" +
			"so name it, even when nothing calls it back.")
	}
	for _, r := range exposes {
		if _, err := parseExpose(r); err != nil {
			return err
		}
	}
	return nil
}

// ---- launcher ----

// launcherRun resolves the cluster, learns its version, and executes the
// matching core binary (downloading it once if needed).
func launcherRun(args []string) {
	opts, cmdArgs := parseArgs(args)
	if len(cmdArgs) == 0 {
		// `plug -p <name>` with no command creates (or reconfigures) that profile.
		// With -H/--port too it's written non-interactively (scriptable); otherwise
		// the wizard asks. Bare `plug` with no -p still just shows usage.
		if len(opts.exposes) > 0 {
			fatal("-s serves a local port for the lifetime of a session — give plug a command to run")
		}
		if opts.profile != "" {
			name := opts.profile
			if opts.host != "" {
				port := opts.port
				if port == "" {
					port = defaultPort
				}
				writeProfile(name, opts.host, port)
			} else {
				name = wizard(name, true)
			}
			info("try it:  plug -p %s <your command>", name)
			return
		}
		fatal("no command given\n\n" + usage())
	}
	if err := serveRequired(opts.exposes); err != nil {
		hint := ""
		if hasServeFlag(cmdArgs) {
			// A -s after the command word was passed TO the command (plug stops
			// parsing its own flags at the first operand) — the likely mistake.
			hint = "\n\nnote: a -s AFTER the command goes to the command; plug's -s must come BEFORE it:\n" +
				"  plug -s <name>:<cluster-port>:<local-port> " + strings.Join(cmdArgs, " ")
		}
		fatal("%s%s\n\n%s", err, hint, usage())
	}
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host or a profile in ~/.plug/")
	}

	remote, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}

	env := coreEnv(cfg)

	// Same version as this launcher (or the agent is unversioned): run in-process.
	if remote == version || remote == "" {
		attachExposes(&cfg, opts.exposes)
		runCore(cfg, cmdArgs)
		return
	}

	// -s is a 2.0.0 feature. A released agent from before it (major < 2) dictates
	// a core that would exec "-s" as the command — an opaque exit 127. Refuse with
	// the remedy instead. (dev/unversioned agents parse as -1 and are assumed new.)
	if maj := coreMajor(remote); len(opts.exposes) > 0 && maj >= 0 && maj < 2 {
		fatal("the cluster agent reports v%s, which predates -s (needs plug ≥ 2.0.0).\n"+
			"Upgrade the agent (redeploy the softwarity/plug image), then run again.", remote)
	}

	bin, err := ensureVersion(remote, cfg)
	if err != nil {
		info("cannot fetch v%s (%v) — falling back to this launcher (v%s)", remote, err, version)
		attachExposes(&cfg, opts.exposes)
		runCore(cfg, cmdArgs)
		return
	}
	info("using cluster version v%s", remote)
	raiseAmbientCaps() // linux: file caps don't survive exec'ing the downloaded core
	// -s mappings cross the exec RAW, as leading argv: the downloaded core owns
	// the grammar (validation included) — this launcher must not veto values a
	// newer core understands. coreMain strips them back. An old launcher doesn't
	// know -s and already forwards them in cmdArgs untouched — same wire format
	// both ways; an old core fails loudly on "-s" instead of silently not
	// exposing.
	for i := len(opts.exposes) - 1; i >= 0; i-- {
		cmdArgs = append([]string{"-s", opts.exposes[i]}, cmdArgs...)
	}
	child := exec.Command(bin, cmdArgs...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = env
	if err := child.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fatal("running v%s: %v", remote, err)
	}
}

// hasServeFlag reports whether a -s / --serve token appears among the command's
// args — used to give a precise hint when -s was placed after the command.
func hasServeFlag(args []string) bool {
	for _, a := range args {
		if a == "-s" || a == "--serve" || strings.HasPrefix(a, "--serve=") {
			return true
		}
	}
	return false
}

// coreMajor parses the leading major version from an agent version string, or
// -1 when it is not a released semver ("dev+<rev>", "", …) — those are assumed
// to be recent builds that understand -s.
func coreMajor(v string) int {
	i := strings.IndexByte(v, '.')
	if i <= 0 {
		return -1
	}
	n, err := strconv.Atoi(v[:i])
	if err != nil {
		return -1
	}
	return n
}

// coreEnv builds the environment for the privileged core re-exec. Host/port/forwards
// travel over PLUG_CORE_* vars — a private launcher→core channel, NOT a user-facing
// option (there is none: the cluster comes from --host or a profile).
//
// COMPAT: launcher and core are routinely DIFFERENT versions (an installed setuid
// launcher execs the cluster's exact core, older or newer — that's the launcher
// model). The env channel is therefore an inter-version protocol: also set the
// legacy PLUG_HOST/PLUG_PORT names so an older downloaded core still finds its
// cluster, and coreMain reads the legacy names as a fallback for older launchers.
func coreEnv(cfg config) []string {
	env := append(os.Environ(), "PLUG_CORE=1",
		"PLUG_CORE_HOST="+cfg.host, "PLUG_CORE_PORT="+cfg.port,
		"PLUG_HOST="+cfg.host, "PLUG_PORT="+cfg.port) // legacy channel for older cores
	if len(cfg.forwards) > 0 {
		var decls []string
		for _, f := range cfg.forwards {
			decls = append(decls, f.decl())
		}
		env = append(env, "PLUG_CORE_FORWARDS="+strings.Join(decls, "\n"),
			"PLUG_FORWARDS="+strings.Join(decls, "\n"))
	}
	return env
}

// coreGetenv reads a launcher→core variable: the current name first, then the
// legacy one (an OLDER launcher exec'ing this newer core only sets the legacy names).
func coreGetenv(name, legacy string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

// runCore executes the tunnel logic in this very process (version match / fallback).
func runCore(cfg config, cmdArgs []string) {
	os.Exit(coreRun(cfg, cmdArgs))
}

func versionsDir() string {
	return filepath.Join(plugDir(), "versions")
}

func ensureVersion(v string, cfg config) (string, error) {
	dir := filepath.Join(versionsDir(), v)
	name := "plug"
	if runtime.GOOS == "windows" {
		name += ".exe" // Windows won't exec a versioned binary without the extension
	}
	bin := filepath.Join(dir, name)
	// Hand the cache back to the user: the setuid helper writes it as euid 0, so
	// without this it lands root-owned (can't be listed/cleaned without sudo). Also
	// self-heals a cache an earlier privileged run already left root-owned.
	own := func() { chownToUser(versionsDir()); chownToUser(dir); chownToUser(bin) }
	if fi, err := os.Stat(bin); err == nil && fi.Size() > 1<<20 {
		own()
		ensureWintunBeside(bin)
		return bin, nil
	}
	data, err := getDownload(cfg, fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH), "v"+v)
	if err != nil {
		return "", err
	}
	if len(data) < 1<<20 || !looksLikeBinary(data) {
		return "", fmt.Errorf("downloaded binary looks invalid (%d bytes)", len(data))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".plug-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Chmod(0o755)
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return "", err
	}
	own()
	ensureWintunBeside(bin)
	return bin, nil
}

// ensureWintunBeside copies wintun.dll next to a versioned binary on Windows.
// WinTUN's loader looks only in the executable's OWN directory (a hardening choice,
// not the PATH), so a binary run from ~/.plug/versions/<v>/ can't find the wintun.dll
// the installer dropped in the launcher dir — "Error loading wintun.dll ... module
// could not be found". Best-effort: copy it from beside the launcher; no-op elsewhere.
func ensureWintunBeside(bin string) {
	if runtime.GOOS != "windows" {
		return
	}
	dst := filepath.Join(filepath.Dir(bin), "wintun.dll")
	if _, err := os.Stat(dst); err == nil {
		return // already there
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(self), "wintun.dll"))
	if err != nil {
		return // launcher has none beside it — nothing to copy
	}
	_ = os.WriteFile(dst, data, 0o644)
}

func listVersions() {
	fmt.Printf("launcher: v%s\n", version)
	entries, err := os.ReadDir(versionsDir())
	if err != nil || len(entries) == 0 {
		fmt.Println("cached: (none yet)")
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	fmt.Printf("cached: %s\n", strings.Join(names, ", "))
}

// ---- get-user helpers (no key: passwordless ForceCommand download) ----

// dialGetUser opens an SSH connection to the agent as the anonymous `get` user,
// using the crypto/ssh library rather than the platform ssh binary.
//
// Why the library: on Windows, shelling out to ssh and capturing its stdout over
// a pipe hangs — OpenSSH forks a child that holds the pipe's write end open past
// ssh's own exit, so the read never sees EOF. crypto/ssh reads the channel
// directly, with no external process or OS pipe, so it behaves the same on every
// platform.
//
// The get user authenticates with "none" (AuthenticationMethods none on the
// agent) and its host key is not pinned — matching the previous
// StrictHostKeyChecking=no behaviour. Key pinning lives on the data tunnel
// (tunnel.Dial), which carries the actual traffic.
func dialGetUser(cfg config) (*ssh.Client, error) {
	return ssh.Dial("tcp", net.JoinHostPort(cfg.host, cfg.port), &ssh.ClientConfig{
		User:            getUser,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	})
}

func agentVersion(cfg config) (string, error) {
	client, err := dialGetUser(cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.Output("version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// updateHold keeps the "updated" line on screen briefly before the child runs
// (the child often clears the screen right away). Overridable in tests.
var updateHold = 400 * time.Millisecond

// minBarDuration keeps the bar animating for at least this long even when the
// transfer is near-instant (the usual case on a LAN) — the whole point is that
// the user actually sees the update happen. Overridable in tests.
var minBarDuration = 2 * time.Second

// getDownload streams a binary from the get-user over SSH. When stderr is a
// terminal it animates a progress bar so a version update is actually visible —
// the transfer is quick and the child usually wipes the screen right after.
// label is the version being fetched, for the display.
func getDownload(cfg config, osArch, label string) ([]byte, error) {
	client, err := dialGetUser(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	if err := sess.Start(osArch); err != nil {
		return nil, err
	}
	data, rerr := readWithProgress(stdout, label, isTTY(os.Stderr))
	if werr := sess.Wait(); werr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%s", s)
		}
		return nil, werr
	}
	return data, rerr
}

// readWithProgress reads r to EOF. When animate is set it draws an indeterminate
// progress bar + byte count on stderr and holds the final line briefly;
// otherwise it prints one plain line. The total size is unknown (the agent just
// streams the binary), hence an indeterminate bar rather than a percentage.
func readWithProgress(r io.Reader, label string, animate bool) ([]byte, error) {
	if !animate {
		fmt.Fprintf(os.Stderr, "[plug] downloading %s from the cluster...\n", label)
	}
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	var frame int
	var last time.Time
	start := time.Now()
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if animate && time.Since(last) > 60*time.Millisecond {
				drawBar(label, int64(buf.Len()), frame)
				frame++
				last = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf.Bytes(), err
		}
	}
	if animate {
		// Keep the bar visible for a minimum duration even when the transfer was
		// near-instant — otherwise the update just flashes by unseen.
		for time.Since(start) < minBarDuration {
			drawBar(label, int64(buf.Len()), frame)
			frame++
			time.Sleep(70 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "\r[plug] ✓ updated to %s  (%s)%s\n",
			label, humanBytes(int64(buf.Len())), strings.Repeat(" ", 14))
		time.Sleep(updateHold)
	} else {
		fmt.Fprintf(os.Stderr, "[plug] updated to %s (%s)\n", label, humanBytes(int64(buf.Len())))
	}
	return buf.Bytes(), nil
}

// drawBar renders one frame of an indeterminate progress bar (a block bouncing
// left↔right), followed by the bytes read so far.
func drawBar(label string, n int64, frame int) {
	const w = 16
	pos := frame % (2 * (w - 1))
	if pos >= w {
		pos = 2*(w-1) - pos
	}
	var b strings.Builder
	for i := 0; i < w; i++ {
		if i >= pos-1 && i <= pos+1 {
			b.WriteRune('█')
		} else {
			b.WriteRune('░')
		}
	}
	fmt.Fprintf(os.Stderr, "\r[plug] updating %s  [%s]  %s ", label, b.String(), humanBytes(n))
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func looksLikeBinary(data []byte) bool {
	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},    // linux
		{0xcf, 0xfa, 0xed, 0xfe}, // macOS 64-bit mach-o
		{0xca, 0xfe, 0xba, 0xbe}, // macOS universal
		{'M', 'Z'},               // windows
	}
	for _, m := range magics {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}

// ---- profiles ----

func parseArgs(args []string) (options, []string) {
	var o options
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(usage())
			os.Exit(0)
		case "-p", "--profile":
			o.profile = flagValue(args, &i)
		case "-H", "--host":
			o.host = flagValue(args, &i)
		case "--port":
			o.port = flagValue(args, &i)
		case "-s", "--serve":
			o.exposes = append(o.exposes, flagValue(args, &i))
		default:
			return o, args[i:]
		}
		i++
	}
	return o, nil
}

func flagValue(args []string, i *int) string {
	if *i+1 >= len(args) {
		fatal("missing value for %s", args[*i])
	}
	*i++
	return args[*i]
}

func resolveConfig(o options) config {
	var cfg config
	switch {
	case o.profile != "" && o.host != "":
		// -p X -H host [--port p]: (re)define profile X from these, then use it —
		// "set it and run" in one line, no wizard.
		port := o.port
		if port == "" {
			port = defaultPort
		}
		writeProfile(o.profile, o.host, port)
		cfg = config{host: o.host, port: port}
	case o.host != "":
	case o.profile != "":
		// An unknown -p profile isn't an error: offer the wizard to create it, so
		// reaching a new cluster is just `plug -p <newname> <cmd>` (no re-install).
		if _, err := os.Stat(filepath.Join(plugDir(), o.profile+".conf")); err != nil {
			info("profile %q doesn't exist yet — let's create it", o.profile)
			cfg = loadProfile(wizard(o.profile, false))
		} else {
			cfg = loadProfile(o.profile)
		}
	default:
		names := listProfiles()
		switch len(names) {
		case 0:
			info("no profile in %s — let's create one", plugDir())
			cfg = loadProfile(wizard("default", false))
		case 1:
			info("using profile %q", names[0])
			cfg = loadProfile(names[0])
		default:
			cfg = loadProfile(chooseProfile(names))
		}
	}
	if o.host != "" {
		cfg.host = o.host
	}
	if o.port != "" {
		cfg.port = o.port
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	return cfg
}

func plugDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("%v", err)
	}
	return filepath.Join(home, ".plug")
}

func listProfiles() []string {
	entries, err := os.ReadDir(plugDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
			names = append(names, strings.TrimSuffix(e.Name(), ".conf"))
		}
	}
	sort.Strings(names)
	return names
}

func loadProfile(name string) config {
	var cfg config
	data, err := os.ReadFile(filepath.Join(plugDir(), name+".conf"))
	if err != nil {
		fatal("profile %q not found in %s", name, plugDir())
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "host":
			cfg.host = val
		case "port":
			cfg.port = val
		case "forward":
			for _, s := range strings.Split(val, ",") {
				if f, ok := parseForward(strings.TrimSpace(s)); ok {
					cfg.forwards = append(cfg.forwards, f)
				}
			}
		}
	}
	return cfg
}

func chooseProfile(names []string) string {
	tty := openTTY("several profiles found, pick one with -p <name>")
	defer tty.Close()
	info("several profiles found:")
	for i, n := range names {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, n)
	}
	in := bufio.NewReader(tty)
	for {
		fmt.Fprintf(os.Stderr, "choose [1-%d]: ", len(names))
		line, err := in.ReadString('\n')
		if err != nil {
			fatal("aborted")
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= len(names) {
			return names[n-1]
		}
	}
}

func wizard(defaultName string, confirmOverwrite bool) string {
	tty := openTTY("cannot run the profile wizard; use --host instead")
	defer tty.Close()
	in := bufio.NewReader(tty)

	name := prompt(in, "profile name", defaultName)
	path := filepath.Join(plugDir(), name+".conf")
	if confirmOverwrite {
		if _, err := os.Stat(path); err == nil {
			if !strings.EqualFold(prompt(in, name+" already exists, overwrite? (y/N)", "n"), "y") {
				fatal("aborted")
			}
		}
	}
	var host string
	for host == "" {
		host = prompt(in, "cluster host", "")
	}
	port := prompt(in, "agent port", defaultPort)

	return writeProfile(name, host, port)
}

// writeProfile saves ~/.plug/<name>.conf with host/port and returns name. Shared
// by the wizard and the non-interactive `plug -p <name> -H <host> [--port <p>]`.
func writeProfile(name, host, port string) string {
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		fatal("%v", err)
	}
	path := filepath.Join(plugDir(), name+".conf")
	content := fmt.Sprintf("host = %s\nport = %s\n", host, port)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fatal("%v", err)
	}
	info("profile %q saved to %s", name, path)
	return name
}

func initProfile() {
	name := wizard("default", true)
	info("try it:  plug -p %s <your command>", name)
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, err := in.ReadString('\n')
	if err != nil {
		fatal("aborted")
	}
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func openTTY(hint string) *os.File {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		fatal("no terminal available — %s", hint)
	}
	return tty
}

// ---- core (the real tunnel work; runs when PLUG_CORE=1 or in-process) ----

func coreMain() {
	cfg := config{
		host: coreGetenv("PLUG_CORE_HOST", "PLUG_HOST"),
		port: coreGetenv("PLUG_CORE_PORT", "PLUG_PORT"),
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	if s := coreGetenv("PLUG_CORE_FORWARDS", "PLUG_FORWARDS"); s != "" {
		for _, line := range strings.Split(s, "\n") {
			if f, ok := parseForward(line); ok {
				cfg.forwards = append(cfg.forwards, f)
			}
		}
	}
	specs, cmdArgs, err := stripLeadingExposes(os.Args[1:])
	if err != nil {
		fatal("%v", err)
	}
	cfg.exposes = specs
	if len(cmdArgs) == 0 {
		fatal("core: no command")
	}
	os.Exit(coreRun(cfg, cmdArgs))
}

func runChild(cmdArgs []string) int {
	return runChildEnv(cmdArgs, nil)
}

// runChildEnv runs the command, optionally with an explicit environment
// (nil = inherit the current one).
func runChildEnv(cmdArgs []string, env []string) int {
	child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if env != nil {
		child.Env = env
	}
	// When plug is the macOS setuid-root helper, this process runs with euid 0 so
	// it can hold the utun + DNS — but YOUR command must not. Drop the child back to
	// the human user (no-op on the Linux caps path). See privdrop_unix.go.
	applyPrivDrop(child)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := child.Start(); err != nil {
		info("cannot start %q: %v", cmdArgs[0], err)
		return 127
	}
	go func() {
		for s := range sigs {
			child.Process.Signal(s)
		}
	}()

	err := child.Wait()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func info(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[plug] "+format+"\n", a...)
}

func fatal(format string, a ...any) {
	info(format, a...)
	os.Exit(1)
}
