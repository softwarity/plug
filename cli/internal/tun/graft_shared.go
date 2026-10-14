//go:build darwin || windows

package tun

import (
	"os"
	"path/filepath"
)

// The cluster readiness and error markers, for the two platforms that hold their
// datapath machine-wide.
//
// They lived in graft_darwin.go and graft_windows.go, and the CODE was identical
// in both, under the build tag this file carries. What differed was what each
// copy explained: one described the daemon and mentioned mirroring the other, the
// other described the service and was alone in saying why the file has to be
// readable without privilege. Whoever read one learned less than whoever read the
// other, and neither said so. Both explanations are here.
//
// Their four neighbours stay per-OS and really are: taking the lock, telling
// whether a daemon is alive, where known_hosts lives, where the ready marker
// goes.

// MarkClusterReady / UnmarkClusterReady are called by the reconcile loop when a
// cluster's tunnel opens or closes, by the daemon on macOS and by the service on
// Windows. ClusterReady lets a `plug -p X <cmd>` wait for its own tunnel before
// running: the datapath is up machine-wide, but the per-cluster tunnel only opens
// on the next reconcile after the client registers.
func MarkClusterReady(key string) { _ = os.WriteFile(readyPath(key), nil, 0o644) }

func UnmarkClusterReady(key string) { _ = os.Remove(readyPath(key)) }

func ClusterReady(key string) bool { _, err := os.Stat(readyPath(key)); return err == nil }

// errorPath records WHY the tunnel for this cluster could not be opened (agent
// unreachable, host key changed…). Written on a failed reconcile and cleared on
// success; a launcher waiting past its timeout shows it instead of a blank "not
// ready". It lives in the shared graft directory, so a launcher holding no
// privilege can still read what the privileged side wrote.
func errorPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".error") }

func MarkClusterError(key, msg string) { _ = os.WriteFile(errorPath(key), []byte(msg), 0o644) }

func ClearClusterError(key string) { _ = os.Remove(errorPath(key)) }

func ClusterError(key string) string {
	b, err := os.ReadFile(errorPath(key))
	if err != nil {
		return ""
	}
	return string(b)
}

// FlushSystemDNS drops the operating system's resolver cache, negative answers
// included. The shared daemon calls it when a cluster's tunnel comes up: a name
// that was in no connected cluster a moment ago may be in this one, and the
// system must ask again rather than repeat the old NXDOMAIN.
func FlushSystemDNS() { flushDNS() }

// The stopping marker is what a datapath that has decided to stop leaves for
// the launchers. Between that decision and the process going away the daemon
// still holds its lock (macOS) or reports StopPending (Windows), while its
// resolver and tunnels are already being taken down: a plug started in that
// window found a daemon "alive", started none, and ran its command with no
// cluster names, or, on Windows, failed to start a service that was stopping.
// A launcher that sees the marker waits for the stop to finish instead, then
// starts a fresh datapath.
func stoppingPath() string { return filepath.Join(graftDir, "daemon.stopping") }

func MarkDaemonStopping() { _ = os.WriteFile(stoppingPath(), nil, 0o644) }

func ClearDaemonStopping() { _ = os.Remove(stoppingPath()) }

func DaemonStopping() bool { _, err := os.Stat(stoppingPath()); return err == nil }
