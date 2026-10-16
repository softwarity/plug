//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The key guards decide on the file a path opens, not on how it is written.
// The rules themselves are pure and tested everywhere (keypathguard_test.go);
// these run the lookups around them on a real Windows filesystem, where one
// file has a short name and may be reached through a junction any user can
// make.

// ownedDir is a directory of the test's own inside the account's profile, with
// a marker the account owns, or a skip where the profile cannot be told.
func ownedDir(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = realPath(t.TempDir())
	marker = filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	profile := profileOfFileOwner(marker)
	if profile == "" || keyOutsideOwnersProfile(dir, realPath(profile)) {
		t.Skipf("the temp directory %s is not in a profile this machine names", dir)
	}
	return dir, marker
}

func shortName(t *testing.T, long string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("no short name for %s (%v)", long, err)
	}
	return windows.UTF16ToString(buf[:n])
}

func TestAKeyNamedInShortFormInItsOwnersProfileIsRead(t *testing.T) {
	dir, marker := ownedDir(t)
	// A name long enough to get an 8.3 alias of its own, so the short form
	// differs from the long one even where the profile's own name is short.
	long := filepath.Join(dir, "a key with a long name", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(long), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(long, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	short := shortName(t, long)
	if strings.EqualFold(short, long) {
		t.Skipf("the volume of %s makes no 8.3 names", long)
	}
	if err := keyOwnerError(short, marker); err != nil {
		t.Errorf("the account's own key, named %s, was refused: %v", short, err)
	}
	if err := keyPathError(short); err != nil {
		t.Errorf("the account's own key, named %s, was refused: %v", short, err)
	}
}

func TestAJunctionInTheProfileDoesNotCarryAKeyOutOfIt(t *testing.T) {
	dir, marker := ownedDir(t)
	windir := os.Getenv("SystemRoot")
	if windir == "" {
		windir = `C:\Windows`
	}
	if _, err := os.Stat(filepath.Join(windir, "win.ini")); err != nil {
		t.Skipf("no win.ini to point at: %v", err)
	}
	// A junction needs no privilege: any account can make one in its profile.
	link := filepath.Join(dir, "j")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, windir).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	// The junction itself, before the temp directory's cleanup: that cleanup
	// must never be the one to find it and walk into the system tree.
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("the junction %s is left in place: %v", link, err)
		}
	})
	key := filepath.Join(link, "win.ini")
	if got, want := realPath(key), realPath(filepath.Join(windir, "win.ini")); !strings.EqualFold(got, want) {
		t.Errorf("realPath(%s) = %s, want the file the junction leads to, %s", key, got, want)
	}
	if err := keyOwnerError(key, marker); err == nil {
		t.Errorf("%s, a file outside the account's profile, was accepted because its path starts inside it", key)
	}
	if err := keyPathError(key); err == nil {
		t.Errorf("%s, a file of the system tree, was accepted because its path starts in a profile", key)
	}
}
