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
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// globalKey is the pseudo-cluster key under which the ONE global daemon holds its
// lock and DNS backup. macOS repoints DNS machine-wide, so a single daemon owns
// the datapath for ALL clusters and routes each flow to the right tunnel by PID
// at connect (docs/multicluster.md). No real cluster key is ever "@global".
const globalKey = "@global"

// daemonMain runs the persistent GLOBAL datapath on macOS: one utun + netstack +
// system-DNS repoint, plus a tunnel PER active cluster, routed by PID at connect.
// It reconciles its tunnel set against the client registry and stays up until no
// cluster has a live client for 30s, or SIGTERM (`plug down`). Runs as root.
func daemonMain(_ []string) int {
	leader, release, _ := tun.AcquireCluster(globalKey)
	if !leader {
		return 0 // another daemon won the start race
	}
	defer release()

	// Crash net, first: repair a DNS a crashed daemon may have left pointing at a
	// dead resolver — else even resolving an agent host below would fail.
	tun.RestoreOrphanDNS(globalKey)

	ct := tun.NewClusterTransports()
	_ = tun.SaveDNSBackup(globalKey) // snapshot the clean DNS before we override it

	dp, err := tun.StartGlobalDatapath(ct, info)
	if err != nil {
		daemonReady(false)
		info("daemon: %v", err)
		return 1
	}

	// First reconcile BEFORE signalling ready, so the cluster that started us has
	// its tunnel open by the time `plug <cmd>` proceeds.
	tunnels := map[string]*tunnel.Transport{}
	reconcileOnce(ct, tunnels)
	daemonReady(true)
	info("daemon: global datapath up (multicluster), %d cluster(s)", len(tunnels))

	stop := make(chan struct{})
	go reconcileLoop(ct, tunnels, stop)
	go reapGlobal(dp, stop)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT) // `plug down`
	go func() { <-sigs; dp.Stop() }()

	dp.Wait()
	close(stop)
	dp.Stop() // idempotent
	closeAll(ct, tunnels)
	tun.ClearDNSBackup(globalKey)
	info("daemon: global datapath down")
	return 0
}

// reconcileLoop re-syncs the tunnel set with the active clusters every 2s.
func reconcileLoop(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, stop <-chan struct{}) {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			reconcileOnce(ct, tunnels)
		}
	}
}

// reconcileOnce opens a tunnel for each active cluster missing one and closes
// tunnels whose cluster no longer has a live client. Each open/close flips the
// cluster's ready marker so `plug -p X <cmd>` can wait for its own tunnel.
func reconcileOnce(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	active := map[string]bool{}
	for _, key := range tun.ActiveClusters() {
		active[key] = true
		if _, have := tunnels[key]; have {
			continue
		}
		host, port, err := net.SplitHostPort(key)
		if err != nil {
			host, port = key, ""
		}
		tr, err := dialTunnel(config{host: host, port: port})
		if err != nil {
			info("daemon: connect %s: %v", key, err)
			continue
		}
		tunnels[key] = tr
		ct.Set(key, tr)
		tun.MarkClusterReady(key)
		info("daemon: tunnel up for %s", key)
	}
	for key, tr := range tunnels {
		if !active[key] {
			tun.UnmarkClusterReady(key)
			ct.Remove(key)
			tr.Close()
			delete(tunnels, key)
			info("daemon: tunnel down for %s", key)
		}
	}
}

// reapGlobal stops the datapath once no cluster has had a live client for grace —
// long enough to ride through a kill+relaunch of a process.
func reapGlobal(dp *tun.Datapath, stop <-chan struct{}) {
	const grace = 30 * time.Second
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var emptySince time.Time
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if len(tun.ActiveClusters()) > 0 {
				emptySince = time.Time{}
				continue
			}
			if emptySince.IsZero() {
				emptySince = time.Now()
				continue
			}
			if time.Since(emptySince) >= grace {
				dp.Stop()
				return
			}
		}
	}
}

func closeAll(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	for key, tr := range tunnels {
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
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

// startDaemonDetached re-execs plug as the DETACHED GLOBAL daemon (new session)
// and blocks until it signals ready (a byte on the pipe) or fails (EOF). Called by
// coreRun when no daemon is up. The daemon serves ALL clusters, so no key is
// passed; cfg is unused beyond triggering the start.
func startDaemonDetached(_ config) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_ = os.MkdirAll("/var/run/plug", 0o755)
	logPath := filepath.Join("/var/run/plug", "daemon.log")
	logf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)

	r, w, err := os.Pipe()
	if err != nil {
		if logf != nil {
			logf.Close()
		}
		return err
	}
	defer r.Close()

	cmd := exec.Command(self, tun.DaemonVerb) // no key → global daemon
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

// cmdDown stops the running global datapath daemon (`plug down`).
func cmdDown(_ []string) {
	if !tun.DaemonAlive(globalKey) {
		tun.RestoreOrphanDNS(globalKey) // repair the DNS if a crashed daemon left it broken
		info("no plug daemon running")
		return
	}
	pid := tun.DaemonPID(globalKey)
	if pid == 0 {
		info("plug daemon: unknown pid")
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fatal("stopping daemon (pid %d): %v", pid, err)
	}
	info("stopped the plug daemon")
}
