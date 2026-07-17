//go:build darwin

package tun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResolvSnapshotRoundTrip covers the /etc/resolv.conf override's crash net: a
// regular file, a symlink (the macOS default → /var/run/resolv.conf) and an absent
// file must each snapshot and restore verbatim. The mesh e2e can't exercise this
// (it never crashes mid-session), so the round-trip is unit-tested here.
func TestResolvSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	orig := resolvConf
	t.Cleanup(func() { resolvConf = orig })
	resolvConf = filepath.Join(dir, "resolv.conf")

	// Regular file → snapshot, override at plug's DNS, restore the original content.
	want := "nameserver 8.8.8.8\nsearch corp.example\n"
	if err := os.WriteFile(resolvConf, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := snapshotResolv()
	writeResolv("198.18.0.53")
	if b, _ := os.ReadFile(resolvConf); !strings.Contains(string(b), "198.18.0.53") || !strings.Contains(string(b), "search plug") {
		t.Fatalf("writeResolv didn't point at plug: %q", b)
	}
	restoreResolv(snap)
	if b, _ := os.ReadFile(resolvConf); string(b) != want {
		t.Fatalf("regular-file restore mismatch: got %q want %q", b, want)
	}

	// Symlink (the usual macOS case) → restore recreates the symlink to the target.
	target := filepath.Join(dir, "run-resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(resolvConf)
	if err := os.Symlink(target, resolvConf); err != nil {
		t.Fatal(err)
	}
	snap = snapshotResolv()
	writeResolv("198.18.0.53")
	if fi, _ := os.Lstat(resolvConf); fi != nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("writeResolv should replace the symlink with a regular file (decoupled from configd)")
	}
	restoreResolv(snap)
	if got, err := os.Readlink(resolvConf); err != nil || got != target {
		t.Fatalf("symlink restore mismatch: got %q err %v want %q", got, err, target)
	}

	// Absent → snapshot "N" → restore leaves it absent.
	_ = os.Remove(resolvConf)
	snap = snapshotResolv()
	writeResolv("198.18.0.53")
	restoreResolv(snap)
	if _, err := os.Lstat(resolvConf); !os.IsNotExist(err) {
		t.Fatalf("absent restore should leave no file, got err %v", err)
	}
}

func TestDNSBackupRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// A real dict → its restore script round-trips verbatim.
	path := filepath.Join(dir, "a.dns.bak")
	key := "State:/Network/Service/gpd.pan/DNS"
	restore := "d.init\nd.add ServerAddresses * 10.10.83.253 172.16.1.225\nd.add SearchDomains * corp.example\n"
	if err := persistDNSBackup(path, key, restore); err != nil {
		t.Fatalf("persist: %v", err)
	}
	gotKey, gotRestore, err := loadDNSBackup(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if gotKey != key || gotRestore != restore {
		t.Fatalf("round-trip mismatch:\n key=%q want %q\n restore=%q want %q", gotKey, key, gotRestore, restore)
	}

	// Empty restore (the service had no DNS dict) → still round-trips (→ remove).
	path2 := filepath.Join(dir, "b.dns.bak")
	if err := persistDNSBackup(path2, key, ""); err != nil {
		t.Fatalf("persist empty: %v", err)
	}
	k2, r2, err := loadDNSBackup(path2)
	if err != nil || k2 != key || r2 != "" {
		t.Fatalf("empty round-trip: key=%q restore=%q err=%v", k2, r2, err)
	}
}

// TestFlushGate covers the DNS-flush debounce: the first effective divergence
// flushes immediately, a storm inside the window collapses into one deferred
// flush, and a quiet gate never fires. The live symptom this guards against — a
// configd event loop restarting mDNSResponder all day — can't run in CI.
func TestFlushGate(t *testing.T) {
	t0 := time.Now()
	g := flushGate{window: 30 * time.Second}

	if g.due(t0) {
		t.Fatal("no request yet — nothing due")
	}
	g.request()
	if !g.due(t0) {
		t.Fatal("first request after a quiet period must fire immediately")
	}
	if g.due(t0) {
		t.Fatal("released — nothing pending anymore")
	}

	// A storm inside the window: requests accumulate, nothing fires...
	for i := 1; i <= 5; i++ {
		g.request()
		if g.due(t0.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("request at +%ds fired inside the 30s window", i)
		}
	}
	// ...until the window passes — then exactly ONE deferred flush.
	if !g.due(t0.Add(31 * time.Second)) {
		t.Fatal("pending flush must fire once the window passed")
	}
	if g.due(t0.Add(32 * time.Second)) {
		t.Fatal("storm must collapse into a single flush")
	}
}
