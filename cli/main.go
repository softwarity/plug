package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

//go:embed keys/id_ed25519
var embeddedKey []byte

// version is stamped at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

const sshUser = "plug"   // tunnel user (public-key)
const getUser = "get"    // download user (passwordless, ForceCommand)
const defaultPort = "2222"
const agentHome = "/opt/plug"

const usageText = `plug — run a local process as if it were inside your cluster network.

Usage:
  plug [options] <command> [args...]   run <command> wired to the cluster
  plug init                            create a profile interactively
  plug ls                              list profiles (name host:port)
  plug test [profile]                  check an agent is reachable
  plug rn <old> <new>                  rename a profile (alias: mv)
  plug rm <profile>                    remove a profile
  plug versions                        list locally cached versions
  plug version                         print the launcher version
  plug self-update                     update the launcher itself from a cluster

Options:
  -p, --profile <name>   use profile ~/.plug/<name>.conf
  -H, --host <host>      agent host (bypasses profiles; also $PLUG_HOST)
      --port <port>      agent SSH port (default 2222; also $PLUG_PORT)
  -h, --help             show this help

How it works:
  plug starts a local SOCKS5 proxy backed by an SSH tunnel to the agent
  and points the child's environment at it (ALL_PROXY, JAVA_TOOL_OPTIONS).
  Cluster names resolve because the proxy hands hostnames to the agent,
  which resolves them inside the cluster. No root, no daemon — so several
  sessions to different clusters run side by side.

  For raw-TCP services whose driver ignores the proxy (AMQP, Kafka…),
  declare a port-forward; plug opens a per-session local port and injects
  its address into the child's environment.

Profiles (~/.plug/*.conf):
  host = swarm-node.example.com
  port = 2222
  forward = AMQP_URL=amqp://rabbitmq:5672, MONGO_URL=mongodb://mongodb:27017

Install from a cluster (no GitHub needed):
  ssh -p 2222 get@<host> install | sh

Docs: https://softwarity.github.io/plug/
`

type config struct {
	host     string
	port     string
	forwards []forwardSpec
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
}

func main() {
	// Core mode: this binary was exec'd by the launcher to do the real work.
	if os.Getenv("PLUG_CORE") == "1" {
		coreMain()
		return
	}

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usageText)
		return
	case "version":
		fmt.Println(version)
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
	case "self-update":
		selfUpdate(args[1:])
		return
	case "uninstall":
		uninstall(args[1:])
		return
	}
	launcherRun(args)
}

// ---- launcher ----

// launcherRun resolves the cluster, learns its version, and executes the
// matching core binary (downloading it once if needed).
func launcherRun(args []string) {
	opts, cmdArgs := parseArgs(args)
	if len(cmdArgs) == 0 {
		fatal("no command given\n\n" + usageText)
	}
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host, $PLUG_HOST or a profile in ~/.plug/")
	}

	remote, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}

	env := coreEnv(cfg)

	// Same version as this launcher (or the agent is unversioned): run in-process.
	if remote == version || remote == "" {
		runCore(cfg, cmdArgs)
		return
	}

	bin, err := ensureVersion(remote, cfg)
	if err != nil {
		info("cannot fetch v%s (%v) — falling back to this launcher (v%s)", remote, err, version)
		runCore(cfg, cmdArgs)
		return
	}
	info("using cluster version v%s", remote)
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

func coreEnv(cfg config) []string {
	env := append(os.Environ(), "PLUG_CORE=1", "PLUG_HOST="+cfg.host, "PLUG_PORT="+cfg.port)
	if len(cfg.forwards) > 0 {
		var decls []string
		for _, f := range cfg.forwards {
			decls = append(decls, f.decl())
		}
		env = append(env, "PLUG_FORWARDS="+strings.Join(decls, "\n"))
	}
	return env
}

// runCore executes the tunnel logic in this very process (version match / fallback).
func runCore(cfg config, cmdArgs []string) {
	os.Exit(coreRunSOCKS(cfg, cmdArgs))
}

func versionsDir() string {
	return filepath.Join(plugDir(), "versions")
}

func ensureVersion(v string, cfg config) (string, error) {
	dir := filepath.Join(versionsDir(), v)
	bin := filepath.Join(dir, "plug")
	if fi, err := os.Stat(bin); err == nil && fi.Size() > 1<<20 {
		return bin, nil
	}
	info("fetching plug v%s from the cluster...", v)
	data, err := getDownload(cfg, fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH))
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
	return bin, nil
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

// selfUpdate replaces the launcher binary itself from a cluster (rare — only
// needed when the bootstrap protocol changes).
func selfUpdate(args []string) {
	opts, _ := parseArgs(args)
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host, $PLUG_HOST or a profile in ~/.plug/")
	}
	remote, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent: %v", err)
	}
	if remote == version {
		info("launcher already at v%s", version)
		return
	}
	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		fatal("%v", err)
	}
	data, err := getDownload(cfg, fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH))
	if err != nil {
		fatal("%v", err)
	}
	if len(data) < 1<<20 || !looksLikeBinary(data) {
		fatal("downloaded binary looks invalid (%d bytes)", len(data))
	}
	tmp, err := os.CreateTemp(filepath.Dir(self), ".plug-self-*")
	if err != nil {
		fatal("%v", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write(data)
	tmp.Chmod(0o755)
	tmp.Close()
	if err := os.Rename(tmp.Name(), self); err != nil {
		fatal("cannot replace %s: %v", self, err)
	}
	info("launcher updated %s → v%s", version, remote)
}

// ---- get-user helpers (no key: passwordless ForceCommand download) ----

func getSSHArgs(cfg config, remoteCmd string) []string {
	return []string{
		"-p", cfg.port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes",
		getUser + "@" + cfg.host, remoteCmd,
	}
}

func agentVersion(cfg config) (string, error) {
	out, err := exec.Command("ssh", getSSHArgs(cfg, "version")...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func getDownload(cfg config, osArch string) ([]byte, error) {
	out, err := exec.Command("ssh", getSSHArgs(cfg, osArch)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
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
			fmt.Print(usageText)
			os.Exit(0)
		case "-p", "--profile":
			o.profile = flagValue(args, &i)
		case "-H", "--host":
			o.host = flagValue(args, &i)
		case "--port":
			o.port = flagValue(args, &i)
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
	case o.host != "" || os.Getenv("PLUG_HOST") != "":
	case o.profile != "":
		cfg = loadProfile(o.profile)
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
	if v := os.Getenv("PLUG_HOST"); v != "" {
		cfg.host = v
	}
	if v := os.Getenv("PLUG_PORT"); v != "" {
		cfg.port = v
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

	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		fatal("%v", err)
	}
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
		host: os.Getenv("PLUG_HOST"),
		port: os.Getenv("PLUG_PORT"),
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	if s := os.Getenv("PLUG_FORWARDS"); s != "" {
		for _, line := range strings.Split(s, "\n") {
			if f, ok := parseForward(line); ok {
				cfg.forwards = append(cfg.forwards, f)
			}
		}
	}
	cmdArgs := os.Args[1:]
	if len(cmdArgs) == 0 {
		fatal("core: no command")
	}
	os.Exit(coreRunSOCKS(cfg, cmdArgs))
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
