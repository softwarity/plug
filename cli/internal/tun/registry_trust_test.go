//go:build darwin || windows

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// What a reader of the registry may believe, and what it must not.
//
// The registry is a directory of files, and that is the point of it: a
// launcher registers without elevation, a daemon that predates a sidecar
// still reads the marker. It is also what makes it a place where a stranger
// can leave a file. Two strangers, in particular. The kernel, which hands a
// crashed launcher's pid number to the next process to ask; and on Windows
// any account on the machine, since %ProgramData%\plug is writable by all.
// The stamp answers the first, the directory name and the file's owner the
// second, and every reader has to ask all of them: clientAccounts did and the
// router did not, so the check that refused a stranger at registration was
// fully in place while the check that routed their traffic read the raw file.

// plantMarker writes a marker the way a launcher would, minus RegisterClient,
// so the test controls every byte: the directory name, the key inside, the
// stamp beside it.
func plantMarker(t *testing.T, dir string, pid int, key, stamp string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.WriteFile(marker, []byte(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if stamp != "" {
		if err := os.WriteFile(marker+startFileSuffix, []byte(stamp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A marker whose stamp says "a process that started long before this one" is a
// dead launcher's, whatever pid now answers to its number. Attributing a flow
// through it would hand this process, under whatever account it runs, the dead
// launcher's cluster and the tunnel opened with the dead launcher's key.
func TestARecycledPIDIsNotAttributedAndItsMarkerIsReaped(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	me := os.Getpid()
	// This process stands in for the recycled pid: alive, and demonstrably not
	// the one that registered, because the stamp predates any machine this runs
	// on. The pins and the key sidecar ride along, as a crashed session leaves them.
	plantMarker(t, clientsDir(key), me, key, "1000000000")
	base := filepath.Join(clientsDir(key), strconv.Itoa(me))
	for _, suffix := range []string{keyFileSuffix, uidFileSuffix, pinsFileSuffix} {
		if err := os.WriteFile(base+suffix, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got, ok := clusterForPID(me); ok {
		t.Fatalf("a recycled pid was attributed to %q: a stranger inherits a dead launcher's cluster", got)
	}
	for _, suffix := range []string{"", keyFileSuffix, uidFileSuffix, startFileSuffix, pinsFileSuffix} {
		if _, err := os.Stat(base + suffix); err == nil {
			t.Errorf("%s%s survived: the marker of a dead launcher must be reaped with every sidecar", filepath.Base(base), suffix)
		}
	}

	// And through ActiveClusters, the reader the daemon reconciles its tunnels
	// against: a cluster whose only client is a recycled pid has no client, so
	// its tunnel, dialled with the dead launcher's key, must come down.
	plantMarker(t, clientsDir(key), me, key, "1000000000")
	if got := ActiveClusters(); len(got) != 0 {
		t.Fatalf("ActiveClusters = %v: a recycled pid keeps a dead launcher's tunnel open", got)
	}
	if _, err := os.Stat(base); err == nil {
		t.Error("ActiveClusters counted a recycled pid out but left its marker behind")
	}
	plantMarker(t, clientsDir(key), me, key, "1000000000")
	if n := LiveClients(key); n != 0 {
		t.Fatalf("LiveClients = %d, want 0 for a recycled pid", n)
	}
}

// And the same marker, stamped with the truth, is answered everywhere: a guard
// that refuses everything closes the hole by removing the feature.
func TestAStampedLiveMarkerIsAnsweredByEveryReader(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	un := RegisterClient(key, os.Getpid(), filepath.Join("home", "dev", ".plug", "keys", "neo"))
	defer un()
	PinName(key, os.Getpid(), "plug-mnt-web-1234abcd")
	pinCache = map[string]pinEntry{}

	if got, ok := clusterForPID(os.Getpid()); !ok || got != key {
		t.Errorf("clusterForPID = %q,%v, want %q,true", got, ok, key)
	}
	if got := ActiveClusters(); len(got) != 1 || got[0] != key {
		t.Errorf("ActiveClusters = %v, want [%s]", got, key)
	}
	if n := LiveClients(key); n != 1 {
		t.Errorf("LiveClients = %d, want 1", n)
	}
	if kf, _ := ClusterKeyFileFrom(key); kf == "" {
		t.Error("ClusterKeyFileFrom found no key for a live, stamped client")
	}
	if got, ok := pinnedCluster("plug-mnt-web-1234abcd"); !ok || got != key {
		t.Errorf("pinnedCluster = %q,%v, want %q,true", got, ok, key)
	}
	// Unregistering takes the pins with it: they were the client's, and a pin
	// that outlives its session becomes active again with the next pid to
	// reuse the number.
	un()
	if _, err := os.Stat(filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())+pinsFileSuffix)); err == nil {
		t.Error("the pins sidecar outlived its client")
	}
}

// A *.clients directory named by hand carries a key of the writer's choosing.
// The name is the one part of a marker the writer does not pick: RegisterClient
// derives it from the key, so a directory whose name is not ClusterHash(key) was
// not written by RegisterClient, and whatever it says about this pid is a lie.
// On Windows anyone can create it; on macOS only root, which makes the check
// cheap insurance there rather than the guard it is on Windows.
func TestAClientDirWithAnArbitraryNameIsIgnored(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "victim.example:2222"
	me := os.Getpid()
	forged := filepath.Join(graftDir, "0000000000000000.clients")
	stamp := ""
	if start, ok := procStart(me); ok {
		stamp = strconv.FormatInt(start, 10) // a live, correctly stamped process: only the name is wrong
	}
	plantMarker(t, forged, me, key, stamp)
	if err := os.WriteFile(filepath.Join(forged, strconv.Itoa(me)+pinsFileSuffix), []byte("postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinCache = map[string]pinEntry{}

	if got, ok := clusterForPID(me); ok {
		t.Errorf("a marker under a directory named by hand attributed this pid to %q", got)
	}
	if got := ActiveClusters(); len(got) != 0 {
		t.Errorf("ActiveClusters = %v: a directory named by hand opened a tunnel", got)
	}
	if got, ok := pinnedCluster("postgres"); ok {
		t.Errorf("a pin under a directory named by hand routed %q to %q", "postgres", got)
	}
	// The real directory for that key, with the same files, is answered: the
	// refusal is about the name, not about the content.
	plantMarker(t, clientsDir(key), me, key, stamp)
	if got, ok := clusterForPID(me); !ok || got != key {
		t.Errorf("clusterForPID = %q,%v under the real directory, want %q,true", got, ok, key)
	}
}

// A marker that names a cluster other than the one its directory is for is the
// same lie told the other way round, and clientAccounts reads it too: the
// account inside must not hold the directory's cluster.
func TestAMarkerNamingAnotherClusterHoldsNothing(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	const key = "host-a:2222"
	plantMarker(t, clientsDir(key), os.Getpid(), "host-b:2222", "")
	if err := os.WriteFile(filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())+uidFileSuffix), []byte(accountA), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(clientAccounts(key)); n != 0 {
		t.Errorf("clientAccounts = %d account(s) from a marker naming another cluster, want 0", n)
	}
	if n := LiveClients(key); n != 0 {
		t.Errorf("LiveClients = %d from a marker naming another cluster, want 0", n)
	}
}
