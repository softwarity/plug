//go:build darwin || windows

package tun

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// The compatibility direction that actually broke a running machine: what THIS
// code writes has to read back correctly under the rule EVERY RELEASED version
// applies, which is TrimSpace over the whole marker.
//
// The daemon is long-lived and runs as root; it survives launches and outlives
// upgrades. So a new core registering a client is read by whatever daemon is
// already in memory. Putting the key on a second line inside the marker made
// that daemon parse "host:2222\n/Users/dev/.plug/keys/neo" as one cluster key,
// dial a host that does not exist, open no tunnel at all, and leave every name
// resolving to a fake IP with nothing behind it. The session looked like a
// broken DNS, not like a registry change.
func TestTheMarkerStaysReadableByEveryOlderDaemon(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"

	un := RegisterClient(key, os.Getpid(), "/home/dev/.plug/keys/neo")
	defer un()

	raw, err := os.ReadFile(filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatal(err)
	}
	// Verbatim the parse every published version does.
	if got := strings.TrimSpace(string(raw)); got != key {
		t.Fatalf("an older daemon reads the marker as %q, want %q.\n"+
			"Anything but the bare cluster key here makes that daemon dial a host that does not exist.", got, key)
	}
	if bytes.ContainsRune(raw, '\n') {
		t.Errorf("the marker gained a line: %q", raw)
	}
}

// The key rides beside the marker, and goes with it.
func TestTheKeySidecarLivesAndDiesWithItsMarker(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"

	un := RegisterClient(key, os.Getpid(), "/home/dev/.plug/keys/neo")
	if got := ClusterKeyFile(key); got != "/home/dev/.plug/keys/neo" {
		t.Fatalf("ClusterKeyFile = %q, want the path the client registered", got)
	}
	un()
	if got := ClusterKeyFile(key); got != "" {
		t.Errorf("the sidecar outlived its client: %q", got)
	}
	entries, err := os.ReadDir(clientsDir(key))
	if err == nil && len(entries) != 0 {
		t.Errorf("unregistering left %d file(s) behind", len(entries))
	}
}

// The sidecar must not be mistaken for a client. It is not a number, which is
// what every scan here parses, but that has to stay true rather than be assumed.
func TestTheSidecarIsNotCountedAsAClient(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"

	un := RegisterClient(key, os.Getpid(), "/home/dev/.plug/keys/neo")
	defer un()

	if n := LiveClients(key); n != 1 {
		t.Errorf("LiveClients = %d, want 1: the sidecar is being counted", n)
	}
	if got := ActiveClusters(); len(got) != 1 || got[0] != key {
		t.Errorf("ActiveClusters = %v, want [%s]", got, key)
	}
	if got, ok := clusterForPID(os.Getpid()); !ok || got != key {
		t.Errorf("clusterForPID = %q,%v, want %q,true", got, ok, key)
	}
}

// A marker written by an older core has no sidecar at all.
func TestAMarkerWithNoSidecarNamesItsClusterAndNoKey(t *testing.T) {
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
		t.Errorf("ClusterKeyFile = %q, want empty for a client that registered none", got)
	}
}

// A profile with no key writes no sidecar, so nothing at all changes for the
// clusters that have always worked.
func TestAClientWithNoKeyWritesTheOldShape(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	key := "cluster.example:2222"
	un := RegisterClient(key, os.Getpid(), "")
	defer un()
	entries, err := os.ReadDir(clientsDir(key))
	if err != nil {
		t.Fatal(err)
	}
	// Not a file count. What protects an ALREADY RUNNING daemon, one that may
	// predate any of these sidecars, is that everything beside the marker carries
	// a name it does not read as a pid: its scans do Atoi on the entry name and
	// skip what fails. Counting files said "one" and meant that, badly, and broke
	// the day a second sidecar was legitimately added. This says the thing
	// itself, so a new sidecar passes and a sidecar named like a pid does not.
	me := strconv.Itoa(os.Getpid())
	sawMarker := false
	for _, e := range entries {
		if e.Name() == me {
			sawMarker = true
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err == nil {
			t.Errorf("%q parses as a pid, so an older daemon would take it for another client's marker", e.Name())
		}
	}
	if !sawMarker {
		t.Errorf("no marker named %q among %d entries", me, len(entries))
	}
	if got := ClusterKeyFile(key); got != "" {
		t.Errorf("ClusterKeyFile = %q, want empty", got)
	}
}
