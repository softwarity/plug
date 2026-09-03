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

// holdCluster plants a live client of key owned by uid, the way RegisterClient
// would if it ran under that account. Written directly because the test cannot
// become another user, and the pid is this process so the liveness check passes.
func holdCluster(t *testing.T, key string, pid int, account string) {
	t.Helper()
	dir := clientsDir(key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.WriteFile(marker, []byte(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker+uidFileSuffix, []byte(account), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestClusterHeldByOther is the whole rule: one cluster belongs to one account.
//
// It exists because the check it replaces did not hold. That one ran per flow and
// compared the connecting account against everyone with a live marker, but a
// client registers BEFORE it authenticates, so a second account only had to run
// `plug -p <the other account's cluster>` to put itself in that set and be waved
// through a tunnel opened with somebody else's key. Refusing the registration is
// what closes it, and closes the multicluster path with it, since that one walks
// the ancestry to a registered launcher and never asked whose it was.
func TestClusterHeldByOther(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"

	if _, held := ClusterHeldByOther(key, accountB); held {
		t.Fatal("an empty registry holds nothing, so nobody is turned away")
	}

	holdCluster(t, key, os.Getpid(), accountA)

	if other, held := ClusterHeldByOther(key, accountB); !held || other != accountA {
		t.Fatalf("a second account must be refused and told who holds it: got (%q,%v), want (%q,true)", other, held, accountA)
	}
	if _, held := ClusterHeldByOther(key, accountA); held {
		t.Fatal("the account that holds the cluster must be able to open a second session on it")
	}
	if _, held := ClusterHeldByOther(key, accountAlways); held {
		t.Fatal("root, or LocalSystem, already owns the machine: exempt, as the flow check had it")
	}
	if _, held := ClusterHeldByOther(key, accountNobody); held {
		t.Fatal("an identity that names nobody cannot be turned away, because it is nobody")
	}
	if _, held := ClusterHeldByOther("host-b:2222", accountB); held {
		t.Fatal("holding one cluster must not hold every cluster")
	}

	// A marker recorded by a client that could not say who it was names nobody,
	// so it must hold nothing against anybody. Checked on the OWNER rather than
	// only on the caller: guarding just the caller made the answer depend on who
	// happened to be asking.
	graftDir = t.TempDir()
	holdCluster(t, key, os.Getpid(), accountNobody)
	if other, held := ClusterHeldByOther(key, accountB); held {
		t.Fatalf("a marker carrying no real identity held the cluster, as %q", other)
	}
}

// TestClusterHeldByADeadClientIsFree keeps the rule from outliving the session it
// protects: a marker whose process is gone must not lock the cluster out of every
// other account until somebody deletes a file by hand.
func TestClusterHeldByADeadClientIsFree(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	dead := spawnAndKill(t)
	holdCluster(t, key, dead, accountA)

	if other, held := ClusterHeldByOther(key, accountB); held {
		t.Fatalf("a dead client (pid %d) still held the cluster, as %q", dead, other)
	}
}

// TestARecycledPIDDoesNotResurrectAMembership is the failure the start stamp
// exists for, and it only became a failure when this marker started REFUSING.
//
// A client that crashes leaves its marker behind. The kernel then reissues pid
// numbers, and the first unrelated process to land on that number makes the dead
// marker look alive again. While membership only granted access that was a small
// leak; now that it turns another account away, it locks the rightful owner out
// of their own cluster with an error naming an account that left.
func TestARecycledPIDDoesNotResurrectAMembership(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"

	// This process stands in for the recycled pid: alive, and demonstrably not
	// the one that registered, because the stamp says the marker's process
	// started long before any machine this runs on was booted.
	holdCluster(t, key, os.Getpid(), accountA)
	stamp := filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())+startFileSuffix)
	if err := os.WriteFile(stamp, []byte("1000000000"), 0o644); err != nil {
		t.Fatal(err)
	}

	if other, held := ClusterHeldByOther(key, accountB); held {
		t.Fatalf("a marker left by a dead client held the cluster, as %q, because a live pid "+
			"reused its number: the start stamp is what tells the two apart", other)
	}
}

// And the same marker, stamped with the truth, must still hold: a stamp that
// refuses everything would close the hole by disabling the rule.
//
// This test used to have a Windows half that asserted the OPPOSITE, because every
// process there reported the same owner and the rule had nobody to tell apart. It
// asserts the same thing on both platforms now, which is the whole point of a
// client recording its token's SID: one sentence, true everywhere, instead of a
// rule and an exception.
func TestAStampedLiveClientStillHoldsTheCluster(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	me := thisAccount()
	if !accountHolds(me) {
		t.Skipf("running as %q, which owns the machine and is exempt by design: "+
			"this test needs an ordinary account to have something to hold", me)
	}

	const key = "host-a:2222"
	un := RegisterClient(key, os.Getpid(), "")
	defer un()

	if _, err := os.Stat(filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())+startFileSuffix)); err != nil {
		t.Fatalf("RegisterClient wrote no start stamp: %v", err)
	}
	if got := readMarkerAccount(t, key, os.Getpid()); got != me {
		t.Fatalf("RegisterClient recorded %q, but this process is %q", got, me)
	}
	// Derived from me rather than a constant: accountA is "501" on macOS, which
	// is an ordinary uid and could be the very account running this test.
	if other, held := ClusterHeldByOther(key, me+"0"); !held || other != me {
		t.Fatalf("a live, correctly stamped client no longer holds its cluster: got (%q,%v), want (%q,true)", other, held, me)
	}
}

// readMarkerAccount is what the marker actually says, so the test can tell a
// client that recorded nothing from one that recorded itself. That difference is
// invisible through ClusterHeldByOther, which answers "not held" either way.
func readMarkerAccount(t *testing.T, key string, pid int) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(clientsDir(key), strconv.Itoa(pid)+uidFileSuffix))
	if err != nil {
		t.Fatalf("reading the owner marker: %v", err)
	}
	return strings.TrimSpace(string(b))
}
