package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A cached version binary must be used as-is — no download — and carry the
// .exe extension on Windows (Windows won't exec a versioned binary without
// it). The fake HOME keeps the real ~/.plug cache untouched.
func TestEnsureVersionCacheHit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp) // what os.UserHomeDir reads on Windows

	name := "plug"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(tmp, ".plug", "versions", "9.9.9")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	// ensureVersion trusts a cached file only above 1 MiB (a truncated download
	// must not be reused) — write 2 MiB.
	if err := os.WriteFile(bin, make([]byte, 2<<20), 0o755); err != nil {
		t.Fatal(err)
	}

	// The cached core is verified against what the agent says it must hash to —
	// it is executed with privilege, so "it is already there" is not enough.
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	saved := fetchDigest
	fetchDigest = func(config, string) (string, error) { return sum, nil }
	defer func() { fetchDigest = saved }()

	got, err := ensureVersion("9.9.9", config{})
	if err != nil {
		t.Fatalf("a cache hit that MATCHES must not error: %v", err)
	}
	if got != bin {
		t.Fatalf("ensureVersion = %q, want the cached %q", got, bin)
	}

	// Same file, a digest that no longer matches: the cache must be discarded
	// rather than executed. The download that follows fails fast here (no
	// cluster), which is exactly how we know it did not reuse the file.
	fetchDigest = func(config, string) (string, error) { return strings.Repeat("a", 64), nil }
	if _, err := ensureVersion("9.9.9", config{}); err == nil {
		t.Fatal("a cached core whose hash does not match was accepted")
	}
	if _, serr := os.Stat(bin); serr == nil {
		t.Error("the mismatching cached core is still on disk — it must be removed, not left to be run")
	}
}

// A too-small cached file must NOT be trusted: ensureVersion falls through to
// the download path, which fails fast here (unreachable host) — proving the
// truncated cache was rejected.
func TestEnsureVersionRejectsTruncatedCache(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	name := "plug"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(tmp, ".plug", "versions", "9.9.8")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 1024), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureVersion("9.9.8", config{host: "127.0.0.1", port: "1"}); err == nil {
		t.Fatal("a truncated cache must not be returned as a valid binary")
	}
}
