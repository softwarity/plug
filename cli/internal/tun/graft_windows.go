//go:build windows

package tun

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows coordination — the mirror of graft_darwin.go. The datapath lives in ONE
// SYSTEM service (see daemon_windows.go), so there is no flock leader election: the
// Service Control Manager already guarantees a single instance. graftDir is a
// machine-wide ProgramData dir shared by the service (SYSTEM) and the non-elevated
// launchers (client + ready markers). The installer grants users write access to it.

// ServiceName is the SCM service that holds the global datapath. Kept in tun so both
// the service (package main) and DaemonAlive agree on it.
const ServiceName = "plug"

// graftDir is machine-wide so SYSTEM and users share it. %ProgramData%\plug.
var graftDir = func() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "plug")
	}
	return `C:\ProgramData\plug`
}()

// ClusterHash is the short, filesystem-safe id for a cluster key (host:port).
func ClusterHash(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// readyPath marks that the service holds a LIVE tunnel for this cluster.
func readyPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".ready") }

// MarkClusterReady / UnmarkClusterReady / ClusterReady let a `plug -p X <cmd>` wait
// for the service to have opened X's tunnel before running (the datapath is up
// service-wide, the per-cluster tunnel opens on the next reconcile).
func MarkClusterReady(key string)   { _ = os.WriteFile(readyPath(key), nil, 0o644) }
func UnmarkClusterReady(key string) { _ = os.Remove(readyPath(key)) }
func ClusterReady(key string) bool  { _, err := os.Stat(readyPath(key)); return err == nil }

// AcquireCluster is a no-op leader on Windows: the datapath is a single SCM service,
// so the service manager is the one owner — no per-cluster flock needed.
func AcquireCluster(_ string) (leader bool, release func(), err error) {
	return true, func() {}, nil
}

// DaemonAlive reports whether the plug SYSTEM service is RUNNING (the Windows notion
// of "a datapath is up"). Uses the least privilege a non-elevated user has —
// SC_MANAGER_CONNECT + SERVICE_QUERY_STATUS — so the launcher can probe without admin.
func DaemonAlive(_ string) bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString(ServiceName)
	if err != nil {
		return false
	}
	sh, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false // not installed, or no access
	}
	defer windows.CloseServiceHandle(sh)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(sh, &st); err != nil {
		return false
	}
	return st.CurrentState == windows.SERVICE_RUNNING
}
