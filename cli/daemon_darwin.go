//go:build darwin

package main

import (
	"fmt"
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

	// Timestamp every daemon.log line: the log is the only forensic trail of a
	// long-lived root daemon, and undated re-assert storms cost a live diagnosis.
	dinfo := func(format string, a ...any) {
		info(time.Now().Format("2006-01-02 15:04:05")+" "+format, a...)
	}

	// Crash net, first: repair a DNS a crashed daemon may have left pointing at a
	// dead resolver — else even resolving an agent host below would fail.
	tun.RestoreOrphanDNS(globalKey)

	ct := tun.NewClusterTransports()
	_ = tun.SaveDNSBackup(globalKey) // snapshot the clean DNS before we override it

	dp, err := tun.StartGlobalDatapath(ct, dinfo)
	if err != nil {
		daemonReady(false)
		dinfo("daemon: %v", err)
		return 1
	}

	// First reconcile BEFORE signalling ready, so the cluster that started us has
	// its tunnel open by the time `plug <cmd>` proceeds.
	tunnels := map[string]*tunnel.Transport{}
	reconcileOnce(ct, tunnels)
	daemonReady(true)
	dinfo("daemon: global datapath up (multicluster), %d cluster(s)", len(tunnels))

	stop := make(chan struct{})
	reconciled := reconcileLoop(ct, tunnels, stop)
	go reapGlobal(dp, stop)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT) // `plug down`
	go func() { <-sigs; dp.Stop() }()

	dp.Wait()
	close(stop)
	dp.Stop()    // idempotent
	<-reconciled // a tick in flight still writes to `tunnels` — let it finish
	closeAll(ct, tunnels)
	tun.ClearDNSBackup(globalKey)
	dinfo("daemon: global datapath down")
	return 0
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

// repairOrphanResolver puts the machine's DNS back when a daemon died without
// tidying up — the resolver still points at plug, nothing answers there, and
// NOTHING resolves system-wide until someone notices.
//
// Done here, silently and on the spot, because `plug update` is exactly when a
// user is already looking: telling them to run another command to repair a
// state they did not cause is how `plug down` ended up being treated as routine
// maintenance. Repairing is safe — it only restores what plug itself saved.
func repairOrphanResolver() {
	if tun.DaemonAlive(globalKey) {
		return // a live daemon owns the override legitimately
	}
	out, _ := exec.Command(tun.HelperPath("scutil"), "--dns").Output()
	if !strings.Contains(string(out), "198.18.") {
		return // not pointed at us: nothing to undo
	}
	tun.RestoreOrphanDNS(globalKey)
	info("the system resolver was still pointed at plug by a daemon that is gone — restored")
}

// cmdDown stops the running global datapath daemon (`plug down`).
func cmdDown(_ []string) {
	if !tun.DaemonAlive(globalKey) {
		tun.RestoreOrphanDNS(globalKey) // repair the DNS if a crashed daemon left it broken
		info("no plug daemon running")
		return
	}
	// What it costs, BEFORE doing it. Killing the daemon pulls the datapath out
	// from under every running session: their connections drop and cluster names
	// stop resolving, while the processes themselves keep running as if nothing
	// happened. Nothing restarts them — there is no watcher. Said plainly here
	// because this command was routinely handed out as an update step, and the
	// sessions it stranded were the reason it "did nothing".
	if m := downStrandsSessions(liveSessions()); m != "" {
		info("%s", m)
	}
	if !stopDaemon() {
		info("plug daemon: unknown pid")
		return
	}
	info("stopped the plug daemon")
}

// stopDaemon sends the daemon its stop signal. Shared by `plug down` and by
// `plug doctor --fix` when it finds one wedged, so both take exactly the same
// path — a repair that differed from the documented command would be its own
// kind of surprise. Reports whether there was a pid to signal.
func stopDaemon() bool {
	pid := tun.DaemonPID(globalKey)
	if pid == 0 {
		return false
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fatal("stopping daemon (pid %d): %v", pid, err)
	}
	return true
}

// liveSessions counts the plug sessions running on this machine, all clusters
// together — the registry is the daemon's own client list.
func liveSessions() int {
	n := 0
	for _, k := range tun.ActiveClusters() {
		n += tun.LiveClients(k)
	}
	return n
}
