package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed keys/id_ed25519
var embeddedKey []byte

// version is stamped at build time via -ldflags "-X main.version=x.y.z".
// Non-semver values ("dev") disable auto-upgrade.
var version = "dev"

const sshUser = "plug"
const defaultPort = "2222"
const agentHome = "/opt/plug"

const usageText = `plug — run a local process as if it were inside your cluster network.

Usage:
  plug [options] <command> [args...]   run <command> with cluster DNS/network
  plug init                            create a profile interactively
  plug upgrade [-f] [options]          update this binary from the agent (-f: allow downgrade)
  plug version                         print the CLI version

Options:
  -p, --profile <name>   use profile ~/.plug/<name>.conf
  -H, --host <host>      agent host (bypasses profiles; also $PLUG_HOST)
      --port <port>      agent SSH port (default 2222; also $PLUG_PORT)
  -h, --help             show this help

Profiles (~/.plug/*.conf):
  Without -p, plug picks the profile automatically: a single profile is
  used as is, several offer an interactive choice, none starts a short
  wizard. Profile format:

    host = swarm-node.example.com
    port = 2222
    # subnets = 10.0.9.0/24,10.0.10.0/24   (optional, skips auto-discovery)
    # auto-upgrade = false                 (optional, default true)

Upgrade:
  On connect, plug compares itself to the agent and silently upgrades if
  the agent ships a NEWER version (never downgrades — with several
  clusters the CLI converges to the newest and stays compatible with
  older agents of the same major). Disable with auto-upgrade = false or
  PLUG_AUTO_UPGRADE=0.

Agent deployment (once, on the cluster):
  https://softwarity.github.io/plug/

Examples:
  plug npm run start:dev
  plug -p staging ./mvnw spring-boot:run
`

type config struct {
	host        string
	port        string
	subnets     []string
	autoUpgrade bool
}

type options struct {
	profile string
	host    string
	port    string
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	switch args[0] {
	case "init":
		initProfile()
		return
	case "version":
		fmt.Println(version)
		return
	case "upgrade":
		upgradeCommand(args[1:])
		return
	}

	opts, cmdArgs := parseArgs(args)
	if len(cmdArgs) == 0 {
		fatal("no command given\n\n" + usageText)
	}
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host, $PLUG_HOST or a profile in ~/.plug/")
	}
	if _, err := exec.LookPath("sshuttle"); err != nil {
		fatal("sshuttle not found — install it first:  brew install sshuttle")
	}

	keyPath, cleanupKey := writeKey()
	defer cleanupKey()
	sshOpts := []string{
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}

	remoteVersion, discovered, err := discover(cfg, sshOpts)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}

	maybeAutoUpgrade(cfg, sshOpts, remoteVersion, cleanupKey)

	subnets := cfg.subnets
	if len(subnets) == 0 {
		subnets = discovered
	}
	if len(subnets) == 0 {
		fatal("no routable subnets found on the agent — is it attached to your overlay networks?")
	}
	info("routing %s via %s:%s", strings.Join(subnets, " "), cfg.host, cfg.port)

	tun, err := startTunnel(cfg, sshOpts, subnets)
	if err != nil {
		fatal("%v", err)
	}
	defer stopTunnel(tun)

	os.Exit(runChild(cmdArgs))
}

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

// resolveConfig picks the connection settings: explicit host first, then the
// requested profile, then automatic profile selection (single → use it,
// several → ask, none → wizard).
func resolveConfig(o options) config {
	cfg := config{autoUpgrade: true}
	switch {
	case o.host != "" || os.Getenv("PLUG_HOST") != "":
		// explicit host, profiles are bypassed entirely
	case o.profile != "":
		cfg = loadProfile(o.profile)
	default:
		names := listProfiles()
		switch len(names) {
		case 0:
			info("no profile in %s — let's create one", profilesDir())
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
	if v := os.Getenv("PLUG_AUTO_UPGRADE"); v == "0" || strings.EqualFold(v, "false") {
		cfg.autoUpgrade = false
	}
	return cfg
}

func profilesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("%v", err)
	}
	return filepath.Join(home, ".plug")
}

func listProfiles() []string {
	entries, err := os.ReadDir(profilesDir())
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
	cfg := config{autoUpgrade: true}
	data, err := os.ReadFile(filepath.Join(profilesDir(), name+".conf"))
	if err != nil {
		fatal("profile %q not found in %s", name, profilesDir())
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
		case "auto-upgrade":
			cfg.autoUpgrade = !(val == "0" || strings.EqualFold(val, "false"))
		case "subnets":
			for _, s := range strings.Split(val, ",") {
				if s = strings.TrimSpace(s); s != "" {
					cfg.subnets = append(cfg.subnets, s)
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

// wizard interactively creates a profile and returns its name.
func wizard(defaultName string, confirmOverwrite bool) string {
	tty := openTTY("cannot run the profile wizard; use --host instead")
	defer tty.Close()
	in := bufio.NewReader(tty)

	name := prompt(in, "profile name", defaultName)
	path := filepath.Join(profilesDir(), name+".conf")
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

	if err := os.MkdirAll(profilesDir(), 0o700); err != nil {
		fatal("%v", err)
	}
	content := fmt.Sprintf("host = %s\nport = %s\n# subnets = 10.0.9.0/24,10.0.10.0/24   (optional, skips auto-discovery)\n# auto-upgrade = false\n", host, port)
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

// openTTY opens the controlling terminal for interactive prompts; reading
// there (not stdin) keeps stdin free for the child process (pipes work).
func openTTY(hint string) *os.File {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		fatal("no terminal available — %s", hint)
	}
	return tty
}

func writeKey() (string, func()) {
	dir, err := os.MkdirTemp("", "plug-")
	if err != nil {
		fatal("%v", err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, embeddedKey, 0o600); err != nil {
		fatal("%v", err)
	}
	return keyPath, func() { os.RemoveAll(dir) }
}

func sshCombined(cfg config, sshOpts []string, remoteCmd string) ([]byte, error) {
	args := append(append([]string{}, sshOpts...),
		"-p", cfg.port, sshUser+"@"+cfg.host, remoteCmd)
	out, err := exec.Command("ssh", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// discover fetches, in one SSH round-trip, the agent version and the routable
// subnets: every non-loopback subnet except the one carrying the default
// route (the docker gateway bridge — services never live there).
func discover(cfg config, sshOpts []string) (string, []string, error) {
	out, err := sshCombined(cfg, sshOpts,
		"cat "+agentHome+"/VERSION 2>/dev/null; echo ---; ip -o -4 addr show; echo ---; ip route show default")
	if err != nil {
		return "", nil, err
	}

	versionPart, rest, _ := strings.Cut(string(out), "---")
	addrPart, routePart, _ := strings.Cut(rest, "---")

	excluded := map[string]bool{"lo": true}
	for _, line := range strings.Split(routePart, "\n") {
		f := strings.Fields(line)
		for j := 0; j < len(f)-1; j++ {
			if f[j] == "dev" {
				excluded[f[j+1]] = true
			}
		}
	}

	var subnets []string
	seen := map[string]bool{}
	for _, line := range strings.Split(addrPart, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] != "inet" {
			continue
		}
		iface := strings.SplitN(f[1], "@", 2)[0]
		if excluded[iface] {
			continue
		}
		_, ipnet, err := net.ParseCIDR(f[3])
		if err != nil || ipnet.IP.IsLoopback() {
			continue
		}
		cidr := ipnet.String()
		if !seen[cidr] {
			seen[cidr] = true
			subnets = append(subnets, cidr)
		}
	}
	return strings.TrimSpace(versionPart), subnets, nil
}

// ---- upgrade ----

// maybeAutoUpgrade replaces this binary and re-execs when the agent ships a
// strictly newer semver. It never downgrades: with several clusters on
// different versions the CLI converges to the newest agent and stays
// compatible with older agents of the same major.
func maybeAutoUpgrade(cfg config, sshOpts []string, remote string, beforeExec func()) {
	if !cfg.autoUpgrade || os.Getenv("PLUG_REEXEC") == "1" || runtime.GOOS == "windows" {
		return
	}
	local, lok := parseSemver(version)
	rem, rok := parseSemver(remote)
	switch {
	case !rok || !lok:
		return // dev builds and unversioned agents never auto-upgrade
	case rem == local:
		return
	case semverLess(rem, local):
		info("agent is older (v%s) than this CLI (v%s) — keeping the newer CLI", remote, version)
		return
	}
	if rem[0] != local[0] {
		fatal("agent v%s and CLI v%s differ in MAJOR version — upgrade manually: plug upgrade", remote, version)
	}
	info("agent ships v%s (CLI is v%s) — upgrading...", remote, version)
	if err := selfReplace(cfg, sshOpts); err != nil {
		info("auto-upgrade failed (%v) — continuing with v%s", err, version)
		return
	}
	info("upgraded to v%s, restarting", remote)
	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	beforeExec()
	env := append(os.Environ(), "PLUG_REEXEC=1")
	if err := syscall.Exec(self, os.Args, env); err != nil {
		fatal("restart failed: %v", err)
	}
}

func upgradeCommand(args []string) {
	var force bool
	var rest []string
	for _, a := range args {
		if a == "-f" || a == "--force" {
			force = true
		} else {
			rest = append(rest, a)
		}
	}
	opts, _ := parseArgs(rest)
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host, $PLUG_HOST or a profile in ~/.plug/")
	}
	keyPath, cleanup := writeKey()
	defer cleanup()
	sshOpts := []string{
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
	out, err := sshCombined(cfg, sshOpts, "cat "+agentHome+"/VERSION")
	if err != nil {
		fatal("cannot read the agent version (old agent image?): %v", err)
	}
	remote := strings.TrimSpace(string(out))
	if remote == version {
		info("already up to date (v%s)", version)
		return
	}
	// Same no-downgrade policy as auto-upgrade, unless -f/--force is given.
	if !force {
		if rem, rok := parseSemver(remote); rok {
			if loc, lok := parseSemver(version); lok && !semverLess(loc, rem) {
				info("this CLI (v%s) is already newer than the agent (v%s) — nothing to do (use -f to force)", version, remote)
				return
			}
		}
	}
	info("agent ships v%s (CLI is %s) — updating...", remote, version)
	if err := selfReplace(cfg, sshOpts); err != nil {
		fatal("%v", err)
	}
	info("updated %s → v%s", version, remote)
}

// selfReplace downloads this platform's binary from the agent over the
// existing SSH channel and atomically swaps it in place.
func selfReplace(cfg config, sshOpts []string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("self-upgrade is not supported on native windows — run plug inside WSL2")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	name := fmt.Sprintf("plug-%s-%s", runtime.GOOS, runtime.GOARCH)
	data, err := sshCombined(cfg, sshOpts, "cat "+agentHome+"/bin/"+name)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	if len(data) < 1<<20 || !looksLikeBinary(data) {
		return fmt.Errorf("downloaded %s looks invalid (%d bytes)", name, len(data))
	}
	tmp, err := os.CreateTemp(filepath.Dir(self), ".plug-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), self)
}

func looksLikeBinary(data []byte) bool {
	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},          // linux
		{0xcf, 0xfa, 0xed, 0xfe},       // macOS arm64/amd64 (64-bit mach-o)
		{0xca, 0xfe, 0xba, 0xbe},       // macOS universal
		{'M', 'Z'},                     // windows
	}
	for _, m := range magics {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func semverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ---- tunnel ----

func startTunnel(cfg config, sshOpts []string, subnets []string) (*exec.Cmd, error) {
	args := []string{
		"-r", fmt.Sprintf("%s@%s:%s", sshUser, cfg.host, cfg.port),
		"--dns",
		"--ssh-cmd", "ssh " + strings.Join(sshOpts, " "),
	}
	args = append(args, subnets...)

	tun := exec.Command("sshuttle", args...)
	stdout, err := tun.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tun.Stderr = tun.Stdout // sshuttle logs on both; merge them
	if err := tun.Start(); err != nil {
		return nil, fmt.Errorf("starting sshuttle: %w", err)
	}

	ready := make(chan struct{})
	failed := make(chan string, 1)
	go func() {
		var lastLine string
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) != "" {
				lastLine = line
			}
			if strings.Contains(line, "Connected") {
				close(ready)
				// keep draining so sshuttle never blocks on a full pipe
				io.Copy(io.Discard, stdout)
				return
			}
			info("sshuttle: %s", line)
		}
		failed <- lastLine
	}()

	info("connecting (sudo may prompt for the local firewall)...")
	select {
	case <-ready:
		info("tunnel up — cluster DNS and subnets are now reachable")
		return tun, nil
	case last := <-failed:
		tun.Wait()
		return nil, fmt.Errorf("sshuttle exited before connecting: %s", last)
	case <-time.After(2 * time.Minute):
		tun.Process.Kill()
		tun.Wait()
		return nil, fmt.Errorf("timed out waiting for the tunnel")
	}
}

func stopTunnel(tun *exec.Cmd) {
	if tun.Process == nil {
		return
	}
	tun.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { tun.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		tun.Process.Kill()
		<-done
	}
	info("tunnel closed")
}

func runChild(cmdArgs []string) int {
	child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

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
