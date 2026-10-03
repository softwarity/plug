package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the host-key callback may do to the file system, and what it must not.
//
// It runs on EVERY dial, and in the macOS daemon that is with euid 0, hours
// after the launcher looked at the path once. Everything it writes has to be
// safe on its own terms at that moment, with the file as it is then and not as
// it was.

const testAddr = "cluster.example:2222"

// A known_hosts that is a symlink is never written through, in either of the
// two places the callback writes: the first pin, and the re-pin of a changed
// key. Replace ~/.plug/known_hosts with a link to a root-owned file, wait for
// the agent to restart, and the old code had root append to that file and then
// rewrite it with the link's content plus one line.
func TestAKnownHostsSymlinkIsNeverWrittenThrough(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	notes := &noteRecorder{}
	cb := tofuHostKey(path, testAddr, notes.logf)
	key1, key2 := genHostKey(t), genHostKey(t)
	// First sight, which pins; then a different key, which re-pins. Both go
	// ahead (the connection is not refused over a file plug will not touch),
	// and neither touches the file.
	if err := cb(testAddr, dummyAddr{}, key1); err != nil {
		t.Fatalf("first sight: %v", err)
	}
	if err := cb(testAddr, dummyAddr{}, key2); err != nil {
		t.Fatalf("changed key: %v", err)
	}

	if got, _ := os.ReadFile(victim); string(got) != "precious\n" {
		t.Fatalf("the file behind the symlink was written through: %q", got)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("known_hosts is no longer the symlink it was (%v, %v): it was replaced", fi, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want only the symlink: a temporary file was left behind", len(entries))
	}
	if !strings.Contains(notes.all(), "left alone") {
		t.Errorf("the person was not told their known_hosts is being ignored; notes:\n%s", notes.all())
	}
}

// A file, or a directory, that belongs to somebody else is not written as
// root. The expected owner is injected (SetKnownHostsOwner) because the suite
// does not run with a privilege to drop; the comparison is the one the daemon
// makes with its real uid.
func TestAKnownHostsOwnedBySomeoneElseIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	me, _, known := fileOwner(fi)
	if !known {
		t.Skip("this platform records no uid, so there is no owner to be wrong about")
	}
	SetKnownHostsOwner(me + 1)
	t.Cleanup(func() { SetKnownHostsOwner(-1) })

	path := filepath.Join(dir, "known_hosts")
	notes := &noteRecorder{}
	cb := tofuHostKey(path, testAddr, notes.logf)
	if err := cb(testAddr, dummyAddr{}, genHostKey(t)); err != nil {
		t.Fatalf("first sight: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a known_hosts was created in a directory that belongs to somebody else")
	}

	// And one that exists but belongs to somebody else is neither believed nor
	// rewritten: the pin inside it is not the person's.
	if err := os.WriteFile(path, []byte(testAddr+" ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cb(testAddr, dummyAddr{}, genHostKey(t)); err != nil {
		t.Fatalf("changed key: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != testAddr+" ssh-ed25519 AAAA\n" {
		t.Fatalf("a known_hosts belonging to somebody else was rewritten: %q", got)
	}
	if !strings.Contains(notes.all(), "not to you") {
		t.Errorf("the refusal was not explained; notes:\n%s", notes.all())
	}

	// Back under the right owner, the same file is read, and re-pinned.
	SetKnownHostsOwner(me)
	if err := cb(testAddr, dummyAddr{}, genHostKey(t)); err != nil {
		t.Fatalf("changed key, right owner: %v", err)
	}
	if got := pinnedKeys(t, path, testAddr); len(got) != 1 || got[0] == "ssh-ed25519 AAAA" {
		t.Fatalf("with the right owner the changed key was not re-pinned: %v", got)
	}
}

// A re-pin lands as a rename, never as a write into the existing file, and
// keeps every other host's line.
func TestARepinReplacesTheFileAndKeepsTheOtherHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := tofuHostKey(path, testAddr, nil)
	other := "other.example:2222 ssh-ed25519 BBBB"
	if err := os.WriteFile(path, []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cb(testAddr, dummyAddr{}, genHostKey(t)); err != nil {
		t.Fatal(err)
	}
	key2 := genHostKey(t)
	if err := cb(testAddr, dummyAddr{}, key2); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), other) {
		t.Errorf("another host's pin was lost in the rewrite:\n%s", data)
	}
	if got := pinnedKeys(t, path, testAddr); len(got) != 1 || got[0] != key2.Type()+" "+sshMarshal(key2) {
		t.Errorf("the re-pin did not land: %v", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("known_hosts is %v, want private to its owner", fi.Mode())
	}
}

// With PLUG_STRICT_HOSTKEY set, a changed key is a refusal, not a note: the
// error names the file and the line to remove, since the person who turned
// this on is the one who will have to act on it after a legitimate restart.
func TestStrictModeRefusesAChangedKey(t *testing.T) {
	t.Setenv("PLUG_STRICT_HOSTKEY", "1")
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb := tofuHostKey(path, testAddr, nil)
	key1, key2 := genHostKey(t), genHostKey(t)
	if err := cb(testAddr, dummyAddr{}, key1); err != nil {
		t.Fatalf("first sight must still pin under strict mode: %v", err)
	}
	if err := cb(testAddr, dummyAddr{}, key1); err != nil {
		t.Fatalf("the pinned key must still be accepted: %v", err)
	}
	err := cb(testAddr, dummyAddr{}, key2)
	if err == nil {
		t.Fatal("a changed key was accepted under PLUG_STRICT_HOSTKEY")
	}
	for _, want := range []string{"PLUG_STRICT_HOSTKEY", testAddr, path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if got := pinnedKeys(t, path, testAddr); len(got) != 1 || got[0] != key1.Type()+" "+sshMarshal(key1) {
		t.Fatalf("the refused key was pinned anyway: %v", got)
	}

	// Off again, the same change is re-pinned as before.
	t.Setenv("PLUG_STRICT_HOSTKEY", "")
	if err := cb(testAddr, dummyAddr{}, key2); err != nil {
		t.Fatalf("without strict mode a changed key must re-pin: %v", err)
	}
}
