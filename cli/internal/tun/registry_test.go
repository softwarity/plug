//go:build darwin || windows

package tun

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestRegistryLiveClients(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	if n := LiveClients(key); n != 0 {
		t.Fatalf("empty registry should have 0 clients, got %d", n)
	}

	un := RegisterClient(key, os.Getpid(), "")
	if n := LiveClients(key); n != 1 {
		t.Fatalf("after register, want 1 live client, got %d", n)
	}

	// A dead PID marker must be reaped, not counted (the kill -9 case).
	RegisterClient(key, spawnAndKill(t), "")
	if n := LiveClients(key); n != 1 {
		t.Fatalf("dead client must be reaped, want 1, got %d", n)
	}

	un()
	if n := LiveClients(key); n != 0 {
		t.Fatalf("after unregister, want 0, got %d", n)
	}
}

func TestRegistryClusterForPID(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-c:2222"
	un := RegisterClient(key, os.Getpid(), "")
	defer un()
	got, ok := clusterForPID(os.Getpid())
	if !ok || got != key {
		t.Fatalf("clusterForPID = %q,%v — want %q,true", got, ok, key)
	}
	if _, ok := clusterForPID(1); ok {
		t.Fatal("an unregistered PID must not map to a cluster")
	}
}

// spawnAndKill starts a process, kills and reaps it, and returns its now-dead PID.
func spawnAndKill(t *testing.T) int {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("ping", "-n", "30", "127.0.0.1")
	} else {
		c = exec.Command("sleep", "30")
	}
	if err := c.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Process.Kill()
	_, _ = c.Process.Wait() // reap so liveness reports dead, not zombie
	return pid
}

// The daemon holds ONE tunnel per cluster and knows a cluster only as host:port.
// It dials on a client's behalf, so the identity it presents has to be that
// client's: without this, an enrolled developer on macOS was refused with the
// shared key's fingerprint even after the launcher and the core had been fixed,
// because the daemon composed a config from a host and a port and nothing else.
func TestTheClientMarkerCarriesTheProfileKeyForTheDaemon(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"

	un := RegisterClient(key, os.Getpid(), "/home/dev/.plug/keys/neo")
	defer un()

	if got := ClusterKeyFile(key); got != "/home/dev/.plug/keys/neo" {
		t.Errorf("ClusterKeyFile = %q, want the path the client registered", got)
	}
	// The cluster key still reads back: the second line must not disturb the first.
	if got := ActiveClusters(); len(got) != 1 || got[0] != key {
		t.Errorf("ActiveClusters = %v, want [%s]", got, key)
	}
	if got, ok := clusterForPID(os.Getpid()); !ok || got != key {
		t.Errorf("clusterForPID = %q,%v, want %q,true", got, ok, key)
	}
}

// A marker written by an older core is one line. It has to read back as "this
// cluster, no personal key" rather than as a cluster whose name includes a path.
func TestAOneLineMarkerStillNamesItsCluster(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"
	dir := clientsDir(key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())), []byte(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ActiveClusters(); len(got) != 1 || got[0] != key {
		t.Errorf("ActiveClusters = %v, want [%s]", got, key)
	}
	if got := ClusterKeyFile(key); got != "" {
		t.Errorf("ClusterKeyFile = %q, want empty for a marker that predates it", got)
	}
}

// A profile with no key registers a one-line marker, so nothing changes for the
// clusters that have always worked.
func TestAClientWithNoKeyWritesTheOldShape(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"
	un := RegisterClient(key, os.Getpid(), "")
	defer un()
	if got := ClusterKeyFile(key); got != "" {
		t.Errorf("ClusterKeyFile = %q, want empty", got)
	}
	if got := ActiveClusters(); len(got) != 1 || got[0] != key {
		t.Errorf("ActiveClusters = %v, want [%s]", got, key)
	}
}
