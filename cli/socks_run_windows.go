//go:build windows

package main

import (
	"os"

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
	return runCoreInProcess(cfg, cmdArgs)
}

// coreRunViaService registers as a client of the cluster, makes sure the service is
// running (started via the SCM, which the installer lets non-admins do), waits for
// the service to open our cluster's tunnel, then runs the child. No tunnel of its own.
func coreRunViaService(cfg config, cmdArgs []string) int {
	key := cfg.host + ":" + cfg.port
	// One cluster, one account. Registering is what makes a process a member,
	// and it happens before anything authenticates, so this is the moment to
	// refuse: see tun.ClusterHeldByOther.
	if other, held := tun.ClusterHeldByOther(key, os.Getuid()); held {
		info(tun.ClusterHeldRefusal, key, other)
		return 1
	}
	unregister := tun.RegisterClient(key, os.Getpid(), cfg.key)
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
