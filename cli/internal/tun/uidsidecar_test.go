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

func TestTheOwnerSidecarRoundTrips(t *testing.T) {
	dir := t.TempDir()
	saved := graftDir
	graftDir = dir
	defer func() { graftDir = saved }()

	const key = "cluster.example:2222"
	cleanup := RegisterClient(key, os.Getpid(), "")

	owners := clientUIDs(key)
	if !owners[os.Getuid()] {
		t.Fatalf("this process registered a client and its account is not in the owner set %v", owners)
	}

	// Gone when the client goes, or the set grows one entry per launch forever
	// and eventually names accounts that left the machine.
	cleanup()
	if len(clientUIDs(key)) != 0 {
		t.Errorf("the owner sidecar outlived its client: %v", clientUIDs(key))
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
	if n := len(clientUIDs(key)); n != 0 {
		t.Fatalf("an older client produced %d owners; it must produce none, so the check stays out of its way", n)
	}
}
