//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// daemonMain runs the persistent datapath for ONE cluster on macOS, where the
// system resolver is global. It holds the leader lock, brings the datapath up,
// tells the launcher it's ready (fd 3), and stays up until no client is left for
// 30s or it gets SIGTERM (`plug down`). Runs as root (re-exec'd from a sudo plug).
// args[0] = "host:port".
func daemonMain(args []string) int {
	if len(args) < 1 {
		return 1
	}
	key := args[0]
	host, port, err := net.SplitHostPort(key)
	if err != nil {
		host, port = key, ""
	}

	// Hold the leader lock for our whole life — that is what makes DaemonAlive true
	// for the clients that graft onto us.
	leader, release, _ := tun.AcquireCluster(key)
	if !leader {
		return 0 // another daemon won the start race
	}
	defer release()

	// Crash net, FIRST: repair a DNS a crashed daemon may have left pointing at a
	// dead resolver — otherwise even resolving the agent host below would fail.
	tun.RestoreOrphanDNS(key)

	tr, err := dialTunnel(config{host: host, port: port})
	if err != nil {
		daemonReady(false)
		info("daemon: connect %s: %v", host, err)
		return 1
	}
	defer tr.Close()

	// Snapshot the (clean) DNS before StartDatapath overrides it.
	_ = tun.SaveDNSBackup(key)

	dp, err := tun.StartDatapath(tr, info)
	if err != nil {
		daemonReady(false)
		info("daemon: %v", err)
		return 1
	}
	daemonReady(true)
	info("daemon: datapath up for %s (DNS %s)", host, dp.DNSIP)

	go reap(key, dp) // stop 30s after the last client leaves
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT) // `plug down`
	go func() { <-sigs; dp.Stop() }()

	dp.Wait()
	dp.Stop() // idempotent
	tun.ClearDNSBackup(key)
	info("daemon: datapath down for %s", host)
	return 0
}

// reap stops the datapath once no client of the cluster has been alive for grace —
// long enough to ride through a `kill + relaunch` of a process.
func reap(key string, dp *tun.Datapath) {
	const grace = 30 * time.Second
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var emptySince time.Time
	for range tk.C {
		if tun.LiveClients(key) > 0 {
			emptySince = time.Time{}
			continue
		}
		if emptySince.IsZero() {
			emptySince = time.Now()
			continue
		}
		if time.Since(emptySince) >= grace {
			if tun.LiveClients(key) > 0 { // a client may have just registered
				emptySince = time.Time{}
				continue
			}
			dp.Stop()
			return
		}
	}
}

// daemonReady signals the launcher over the inherited pipe (fd 3): a byte means
// the datapath is up, EOF-without-a-byte means it failed to come up.
func daemonReady(ok bool) {
	f := os.NewFile(3, "ready")
	if f == nil {
		return
	}
	if ok {
		_, _ = f.Write([]byte{1})
	}
	_ = f.Close()
}

// startDaemonDetached re-execs plug as a DETACHED daemon (new session) for the
// cluster and blocks until it signals ready (a byte on the pipe) or fails (EOF).
// Called by coreRun (macOS) when no daemon is up yet.
func startDaemonDetached(cfg config) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_ = os.MkdirAll("/var/run/plug", 0o755)
	logPath := filepath.Join("/var/run/plug", tun.ClusterHash(cfg.host+":"+cfg.port)+".log")
	logf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)

	r, w, err := os.Pipe()
	if err != nil {
		if logf != nil {
			logf.Close()
		}
		return err
	}
	defer r.Close()

	cmd := exec.Command(self, tun.DaemonVerb, cfg.host+":"+cfg.port)
	cmd.Env = append(os.Environ(), "PLUG_CORE=1")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.ExtraFiles = []*os.File{w} // becomes fd 3 in the daemon
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		if logf != nil {
			logf.Close()
		}
		return err
	}
	w.Close() // parent drops its copy — only the daemon holds the write-end
	if logf != nil {
		logf.Close()
	}

	_ = r.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 1)
	if n, _ := r.Read(buf); n == 1 {
		return nil // ready
	}
	// Surface the daemon's own failure reason (its last log line) instead of an
	// opaque "did not come up".
	if reason := lastLogLine(logPath); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	return fmt.Errorf("daemon did not come up (see %s)", logPath)
}

// lastLogLine returns the last non-empty line of the daemon log — its failure
// reason — stripped of the "[plug] " prefix.
func lastLogLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), "[plug] ")
}

// cmdDown stops the running datapath daemon for the resolved cluster (`plug down`).
func cmdDown(args []string) {
	opts, _ := parseArgs(args)
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host, $PLUG_HOST or a profile")
	}
	key := cfg.host + ":" + cfg.port
	if !tun.DaemonAlive(key) {
		tun.RestoreOrphanDNS(key) // repair the DNS if a crashed daemon left it broken
		info("no plug daemon running for %s", cfg.host)
		return
	}
	pid := tun.DaemonPID(key)
	if pid == 0 {
		info("plug daemon for %s: unknown pid", cfg.host)
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fatal("stopping daemon (pid %d): %v", pid, err)
	}
	info("stopped the plug daemon for %s", cfg.host)
}
