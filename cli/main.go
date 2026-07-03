package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed keys/id_ed25519
var embeddedKey []byte

const sshUser = "plug"
const defaultPort = "2222"

const usageText = `plug — run a local process as if it were inside your cluster network.

Usage:
  plug [options] <command> [args...]   run <command> with cluster DNS/network
  plug init                            write an example plug-stack.yml here

Options:
  -p, --profile <name>   profile from ~/.plug/<name>.conf (default: "default")
  -H, --host <host>      agent host (overrides profile / $PLUG_HOST)
      --port <port>      agent SSH port (default 2222, overrides profile / $PLUG_PORT)
  -h, --help             show this help

Profile file (~/.plug/<name>.conf):
  host = swarm-node.example.com
  port = 2222
  # optional, skips auto-discovery:
  # subnets = 10.0.9.0/24,10.0.10.0/24

Examples:
  plug npm run start:dev
  plug -p staging ./mvnw spring-boot:run
`

const stackTemplate = `# plug agent — deploy with:  docker stack deploy -c plug-stack.yml plug
# Attach it to every overlay network holding services you want to reach.
services:
  agent:
    image: ghcr.io/softwarity/plug-agent:latest
    ports:
      - "2222:22"
    networks:
      - app_net
    deploy:
      replicas: 1

networks:
  app_net:
    external: true
`

type config struct {
	host    string
	port    string
	subnets []string
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	if args[0] == "init" {
		initStack()
		return
	}

	cfg, cmdArgs := parseArgs(args)
	if len(cmdArgs) == 0 {
		fatal("no command given\n\n" + usageText)
	}
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

	subnets := cfg.subnets
	if len(subnets) == 0 {
		var err error
		subnets, err = discoverSubnets(cfg, sshOpts)
		if err != nil {
			fatal("cannot discover cluster subnets via %s:%s: %v", cfg.host, cfg.port, err)
		}
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

func parseArgs(args []string) (config, []string) {
	profile := "default"
	var host, port string
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(usageText)
			os.Exit(0)
		case "-p", "--profile":
			profile = flagValue(args, &i)
		case "-H", "--host":
			host = flagValue(args, &i)
		case "--port":
			port = flagValue(args, &i)
		default:
			goto done
		}
		i++
	}
done:
	cfg := loadProfile(profile)
	if v := os.Getenv("PLUG_HOST"); v != "" {
		cfg.host = v
	}
	if v := os.Getenv("PLUG_PORT"); v != "" {
		cfg.port = v
	}
	if host != "" {
		cfg.host = host
	}
	if port != "" {
		cfg.port = port
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	return cfg, args[i:]
}

func flagValue(args []string, i *int) string {
	if *i+1 >= len(args) {
		fatal("missing value for %s", args[*i])
	}
	*i++
	return args[*i]
}

func loadProfile(name string) config {
	var cfg config
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(home, ".plug", name+".conf"))
	if err != nil {
		if name != "default" {
			fatal("profile %q not found in ~/.plug/", name)
		}
		return cfg
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

// discoverSubnets asks the agent for its interfaces and default route, then
// keeps every non-loopback subnet except the one carrying the default route
// (the docker gateway bridge — services never live there).
func discoverSubnets(cfg config, sshOpts []string) ([]string, error) {
	args := append(append([]string{}, sshOpts...),
		"-p", cfg.port, sshUser+"@"+cfg.host,
		"ip -o -4 addr show; echo ---; ip route show default")
	out, err := exec.Command("ssh", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}

	addrPart, routePart, _ := strings.Cut(string(out), "---")
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
	return subnets, nil
}

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

func initStack() {
	const name = "plug-stack.yml"
	if _, err := os.Stat(name); err == nil {
		fatal("%s already exists here, not overwriting", name)
	}
	if err := os.WriteFile(name, []byte(stackTemplate), 0o644); err != nil {
		fatal("%v", err)
	}
	fmt.Printf(`wrote %s

next steps:
  1. edit the networks section to list your overlay networks
  2. docker stack deploy -c %s plug
  3. plug --host <any-swarm-node> <your command>
`, name, name)
}

func info(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[plug] "+format+"\n", a...)
}

func fatal(format string, a ...any) {
	info(format, a...)
	os.Exit(1)
}
