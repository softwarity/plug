//go:build darwin || windows

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The owner travels BESIDE the marker, never inside it, and this test exists
// because the same mistake was already made once with the key sidecar. A daemon
// that is already running is what reads these files: it may predate this code by
// weeks, and it does TrimSpace over the whole marker. A second line inside it
// becomes part of the cluster key, the daemon then dials a host that does not
// exist, opens no tunnel, and every name resolves to a fake IP with nothing
// behind it. The marker must stay byte-identical to what every released version
// writes.
func TestTheOwnerSidecarLeavesTheMarkerAlone(t *testing.T) {
	dir := t.TempDir()
	saved := graftDir
	graftDir = dir
	defer func() { graftDir = saved }()

	const key = "cluster.example:2222"
	pid := os.Getpid()
	cleanup := RegisterClient(key, pid, "")
	defer cleanup()

	marker := filepath.Join(clientsDir(key), strconv.Itoa(pid))
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("no marker written: %v", err)
	}
	// Read the way a daemon released before any sidecar existed reads it.
	if string(body) != key {
		t.Fatalf("the marker reads %q, an older daemon would take that for the cluster key instead of %q", body, key)
	}
}

// What a client WRITES is what the owner set READS. That contract is the whole
// mechanism, and it is asserted through clientAccounts rather than clientUIDs
// because the two are no longer the same reader: an account is a uid here and a
// SID on Windows, so the set that decides who holds a cluster reads text.
func TestTheOwnerSidecarRoundTrips(t *testing.T) {
	dir := t.TempDir()
	saved := graftDir
	graftDir = dir
	defer func() { graftDir = saved }()

	const key = "cluster.example:2222"
	cleanup := RegisterClient(key, os.Getpid(), "")

	owners := clientAccounts(key)
	if !owners[thisAccount()] {
		t.Fatalf("this process registered a client and its account %q is not in the owner set %v", thisAccount(), owners)
	}

	// Gone when the client goes, or the set grows one entry per launch forever
	// and eventually names accounts that left the machine.
	cleanup()
	if len(clientAccounts(key)) != 0 {
		t.Errorf("the owner sidecar outlived its client: %v", clientAccounts(key))
	}
}

// clientUIDs is the NUMERIC view of that same set, and it is the only reader the
// per-flow check has. On macOS it holds the uid. On Windows it is empty, because
// a SID is not a number, and that is not a regression to fix: uidOf reports
// failure on that platform, so the flow check already fell through there when the
// set held the -1 every client used to record. This test says so out loud, since
// an empty set looks like a bug to anyone reading clientUIDs on its own.
func TestTheNumericOwnerViewIsPlatformShaped(t *testing.T) {
	dir := t.TempDir()
	saved := graftDir
	graftDir = dir
	defer func() { graftDir = saved }()

	const key = "cluster.example:2222"
	cleanup := RegisterClient(key, os.Getpid(), "")
	defer cleanup()

	uids := clientUIDs(key)
	if _, numeric := strconv.Atoi(thisAccount()); numeric == nil {
		if !uids[os.Getuid()] {
			t.Fatalf("an account that IS a number must appear in the numeric view: %v", uids)
		}
		return
	}
	if len(uids) != 0 {
		t.Fatalf("an account that is not a number cannot appear in the numeric view, got %v", uids)
	}
}

// A client written by a version that did not record its owner must read as
// UNKNOWN, which is what keeps such a client working rather than cutting it off.
func TestAClientWithoutAnOwnerSidecarReadsAsUnknown(t *testing.T) {
	dir := t.TempDir()
	saved := graftDir
	graftDir = dir
	defer func() { graftDir = saved }()

	const key = "cluster.example:2222"
	if err := os.MkdirAll(clientsDir(key), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exactly what an older plug leaves behind: the marker, and nothing else.
	if err := os.WriteFile(filepath.Join(clientsDir(key), strconv.Itoa(os.Getpid())), []byte(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(clientAccounts(key)); n != 0 {
		t.Fatalf("an older client produced %d owners; it must produce none, so the check stays out of its way", n)
	}
}
