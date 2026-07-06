//go:build darwin

package tun

import (
	"path/filepath"
	"testing"
)

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
