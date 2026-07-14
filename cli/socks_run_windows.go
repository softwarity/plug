//go:build windows

package main

import (
	"os"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"golang.org/x/sys/windows"
)

// coreRun on Windows picks the datapath model at runtime:
//
//   - service installed → delegate to the SYSTEM service (daemon_windows.go). No
//     admin, and multicluster: the service holds N tunnels and attributes each flow
//     by PID at connect. This is the "run without admin" path.
//   - not installed     → hold the datapath in THIS (elevated) process for the
//     child's lifetime. Single-cluster; needs an elevated terminal. This is the
//     original path — validated first — kept as a fallback so plug always works even
//     before the service is set up (or if it fails).
func coreRun(cfg config, cmdArgs []string) int {
	if serviceInstalled() {
		return coreRunViaService(cfg, cmdArgs)
	}
	return coreRunInProcess(cfg, cmdArgs)
}

// coreRunViaService registers as a client of the cluster, makes sure the service is
// running (started via the SCM, which the installer lets non-admins do), waits for
// the service to open our cluster's tunnel, then runs the child. No tunnel of its own.
func coreRunViaService(cfg config, cmdArgs []string) int {
	key := cfg.host + ":" + cfg.port
	unregister := tun.RegisterClient(key, os.Getpid())
	defer unregister()

	if !tun.DaemonAlive(globalKey) {
		if err := startService(); err != nil && !tun.DaemonAlive(globalKey) {
			info("cannot start the plug service: %v", err)
			info("try:  plug down  then retry, or reinstall the service (plug install-service, elevated).")
			return 1
		}
	}
	waitClusterReady(key)
	// The reverse direction is per-session, not per-cluster: it rides its own
	// transport in THIS process, not the SYSTEM service — Ctrl-C closes the port.
	stopExposes, err := startExposes(cfg)
	if err != nil {
		info("expose: %v", err)
		return 1
	}
	defer stopExposes()
	return runChildEnv(cmdArgs, nil)
}

// coreRunInProcess holds the datapath in this elevated process for the child's
// lifetime — the single-cluster fallback (the pre-service Windows path).
func coreRunInProcess(cfg config, cmdArgs []string) int {
	tr, err := dialTunnel(cfg)
	if err != nil {
		info("connect: %v", err)
		return 1
	}
	defer tr.Close()
	stopExposes, err := startExposes(cfg)
	if err != nil {
		info("expose: %v", err)
		return 1
	}
	defer stopExposes()
	info("tunnel ready — running your command")
	code, rerr := tun.Run(tr, cmdArgs, info)
	if rerr != nil {
		info("%v", rerr)
	}
	return code
}

// waitClusterReady blocks until the service opened OUR cluster's tunnel (its ready
// marker) or a short timeout, then runs the child anyway (best effort).
func waitClusterReady(key string) {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if tun.ClusterReady(key) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	if msg := tun.ClusterError(key); msg != "" {
		info("cluster %s: %s", key, msg)
		return
	}
	info("cluster %s: tunnel not ready yet — starting anyway", key)
}

// serviceInstalled reports whether the SCM service exists, regardless of run state.
func serviceInstalled() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString(tun.ServiceName)
	if err != nil {
		return false
	}
	sh, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(sh)
	return true
}
