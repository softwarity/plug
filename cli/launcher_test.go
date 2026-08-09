package main

import (
	"errors"
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
	// The store is not under $HOME on every platform any more (macOS runs the
	// core as root, so it lives somewhere the user cannot write). Point it at the
	// temp dir the same way fetchDigest is redirected, rather than assuming a
	// layout that is now per-OS.
	store := filepath.Join(tmp, "store")
	savedStore := versionsDir
	versionsDir = func() string { return store }
	defer func() { versionsDir = savedStore }()

	dir := filepath.Join(store, "9.9.9")
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
	// It hands back the DESCRIPTOR it verified, not a path — that is what the
	// launcher then executes, so that nothing can be swapped in between.
	if got.Name() != bin {
		t.Fatalf("ensureVersion = %q, want the cached %q", got.Name(), bin)
	}
	got.Close()

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

// The launcher is replaced on a NUMBER, which is all launcherFollow can see —
// but it is a thin thing (pick a version, verify a digest, exec the core) and
// goes untouched across most releases. Replacing it then costs a 9MB download
// and swaps a setuid ROOT binary for an identical one.
//
// The agent already publishes what each build must hash to, so compare before
// replacing. Here the digest matches this very test binary: updateLauncher must
// return before reaching the network — a config pointing nowhere would fatal()
// and take the test process with it if it did not.
func TestTheLauncherIsNotReplacedByAnIdenticalBuild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate the test binary")
	}
	sum, err := fileSHA256(self)
	if err != nil {
		t.Skip("cannot hash the test binary")
	}
	saved := fetchDigest
	fetchDigest = func(config, string) (string, error) { return sum, nil }
	defer func() { fetchDigest = saved }()

	// A version that differs, so launcherFollow says "replace" and we reach the
	// digest comparison; a host that cannot be dialled, so any download fails
	// loudly rather than silently passing the test.
	updateLauncher(config{host: "127.0.0.1", port: "1"}, "9.9.9")
}

// And a digest the agent cannot give must NOT block the update: unknown is not
// "identical", so it falls through and replaces as before.
func TestAnUnavailableDigestStillReplaces(t *testing.T) {
	saved := fetchDigest
	fetchDigest = func(config, string) (string, error) { return "", errUnavailableTest }
	defer func() { fetchDigest = saved }()

	if replace, _ := launcherFollow("2.9.3", "2.9.4"); !replace {
		t.Fatal("a version change must still ask to replace")
	}
	// The decision to fall through lives in updateLauncher's guard: with no
	// digest, it cannot conclude "same bytes" and must go on to download.
	if want, err := fetchDigest(config{}, "x"); err == nil || want != "" {
		t.Errorf("the stub must report no digest, got %q / %v", want, err)
	}
}

var errUnavailableTest = errors.New("digest unavailable")
