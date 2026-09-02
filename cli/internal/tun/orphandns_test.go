//go:build darwin

package tun

import (
	"os"
	"strings"
	"testing"
)

// RestoreOrphanDNS is what repairs the resolver of the WHOLE MACHINE after a
// daemon dies without unwinding: killed with -9, out of memory, a panic in a
// goroutine that skipped every defer. Until now nothing exercised it, so the
// order it replays the three backups in, and whether it cleans up after itself,
// were whatever the code happened to do.
//
// The order matters. The service dictionaries have to go back before the global
// override is dropped, because configd recomposes the global FROM them: drop it
// first and there is a window with no resolver at all, on a machine whose owner
// has just had plug die under them.
func TestOrphanRecoveryPutsTheResolverBackInOrder(t *testing.T) {
	dir := t.TempDir()
	savedDir := graftDir
	graftDir = dir
	defer func() { graftDir = savedDir }()

	var seq []string
	sSet, sRemove, sResolv := scutilSet, scutilRemove, restoreResolv
	defer func() { scutilSet, scutilRemove, restoreResolv = sSet, sRemove, sResolv }()
	scutilSet = func(key, _ string) error { seq = append(seq, "set "+key); return nil }
	scutilRemove = func(key string) error { seq = append(seq, "remove "+key); return nil }
	restoreResolv = func(string) { seq = append(seq, "resolv") }

	const key = "cluster.example:2222"
	write(t, resolvBackupPath(key), "L\n/etc/resolv.conf.orig")
	write(t, setupBackupPath(key), "Setup:/Network/Service/X/DNS\nd.init\nd.add ServerAddresses * 10.0.0.1\n")
	write(t, backupPath(key), "State:/Network/Service/X/DNS\nd.init\nd.add ServerAddresses * 10.0.0.1\n")

	RestoreOrphanDNS(key)

	want := []string{
		"resolv",                           // the file a static binary reads, first
		"set Setup:/Network/Service/X/DNS", // what the user configured by hand
		"set State:/Network/Service/X/DNS", // what the network handed them
		"remove State:/Network/Global/DNS", // ours, dropped last
	}
	if strings.Join(seq, " | ") != strings.Join(want, " | ") {
		t.Errorf("the recovery replayed\n  %v\nwant\n  %v\nThe global override must go LAST: configd "+
			"recomposes it from the service dictionaries, so dropping it first leaves a window with no "+
			"resolver at all on a machine whose owner just had plug die under them", seq, want)
	}

	// And it must not leave its own backups behind: the next launch would replay
	// a resolver that has since moved on, undoing whatever the user did in between.
	for _, p := range []string{resolvBackupPath(key), setupBackupPath(key), backupPath(key)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the recovery, so the next launch will replay a stale resolver over "+
				"whatever the machine has now", p)
		}
	}
}

// A backup that is present but EMPTY means the service had no DNS dictionary of
// its own before plug touched it. Putting an empty one back would pin the service
// to nothing; the entry has to be removed instead.
func TestAnEmptyBackupRemovesRatherThanRestores(t *testing.T) {
	dir := t.TempDir()
	savedDir := graftDir
	graftDir = dir
	defer func() { graftDir = savedDir }()

	var seq []string
	sSet, sRemove, sResolv := scutilSet, scutilRemove, restoreResolv
	defer func() { scutilSet, scutilRemove, restoreResolv = sSet, sRemove, sResolv }()
	scutilSet = func(key, _ string) error { seq = append(seq, "set "+key); return nil }
	scutilRemove = func(key string) error { seq = append(seq, "remove "+key); return nil }
	restoreResolv = func(string) {}

	const key = "cluster.example:2222"
	write(t, backupPath(key), "State:/Network/Service/X/DNS\n")

	RestoreOrphanDNS(key)

	for _, s := range seq {
		if strings.HasPrefix(s, "set State:/Network/Service/") {
			t.Fatalf("an empty backup was written back as a dictionary (%s), which pins that service to "+
				"no resolver at all instead of leaving it as it was", s)
		}
	}
	if len(seq) == 0 || seq[0] != "remove State:/Network/Service/X/DNS" {
		t.Errorf("an empty backup should REMOVE the entry it belongs to, got %v", seq)
	}
}

// Nothing to restore must do nothing, quietly. Every launch calls this for every
// cluster it knows, so a missing backup is the normal case, not an error.
func TestNoBackupMeansNoAction(t *testing.T) {
	dir := t.TempDir()
	savedDir := graftDir
	graftDir = dir
	defer func() { graftDir = savedDir }()

	var touched int
	sSet, sRemove, sResolv := scutilSet, scutilRemove, restoreResolv
	defer func() { scutilSet, scutilRemove, restoreResolv = sSet, sRemove, sResolv }()
	scutilSet = func(string, string) error { touched++; return nil }
	scutilRemove = func(string) error { touched++; return nil }
	restoreResolv = func(string) { touched++ }

	RestoreOrphanDNS("cluster.example:2222")
	if touched != 0 {
		t.Errorf("a cluster with no backup still touched the machine's resolver %d times", touched)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
