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

// readKeyFile decides on the file it opened, not on how its path is written.
// The rules themselves are pure and tested everywhere (keypathguard_test.go);
// these run them on a real Windows filesystem, where one file has a short name
// and may be reached through a junction any user can make. The profile the
// key must lie in is a directory of the test's own: the rule is containment,
// whoever's profile it is.

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

// junction makes link a directory junction to target, as any account may in
// its own profile, and removes it before the temp directory's cleanup: that
// cleanup must never be the one to find it and walk into where it leads.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("the junction %s is left in place: %v", link, err)
		}
	})
}

func writeKey(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAKeyNamedInShortFormInItsOwnersProfileIsRead(t *testing.T) {
	profile := realPath(t.TempDir())
	// A name long enough to get an 8.3 alias of its own, so the short form
	// differs from the long one even where the profile's own name is short.
	long := filepath.Join(profile, "a key with a long name", "id_ed25519")
	writeKey(t, long)
	short := shortName(t, long)
	if strings.EqualFold(short, long) {
		t.Skipf("the volume of %s makes no 8.3 names", long)
	}
	t.Logf("reading %s, the short name of %s", short, long)
	got, err := readKeyFile(short, profile)
	if err != nil {
		t.Fatalf("the account's own key, named %s, was refused: %v", short, err)
	}
	if string(got) != "key" {
		t.Fatalf("read %q from %s, want the key written there", got, short)
	}
}

func TestAJunctionInTheProfileDoesNotCarryAKeyOutOfIt(t *testing.T) {
	dir := realPath(t.TempDir())
	profile, elsewhere := filepath.Join(dir, "home"), filepath.Join(dir, "elsewhere")
	writeKey(t, filepath.Join(elsewhere, "id_ed25519"))
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	junction(t, filepath.Join(profile, "j"), elsewhere)
	key := filepath.Join(profile, "j", "id_ed25519")
	if _, err := readKeyFile(key, profile); !isLocalRefusal(err) {
		t.Errorf("%s, a file outside the profile, was not refused (%v): its path only starts inside it", key, err)
	}
}

func TestAJunctionDoesNotCarryAKeyIntoTheSystemTree(t *testing.T) {
	windir := os.Getenv("SystemRoot")
	if windir == "" {
		windir = `C:\Windows`
	}
	if _, err := os.Stat(filepath.Join(windir, "win.ini")); err != nil {
		t.Skipf("no win.ini to point at: %v", err)
	}
	link := filepath.Join(realPath(t.TempDir()), "j")
	junction(t, link, windir)
	key := filepath.Join(link, "win.ini")
	// No profile to lie in: the system tree is refused to everyone.
	if _, err := readKeyFile(key, ""); !isLocalRefusal(err) {
		t.Errorf("%s, a file of the system tree, was not refused (%v)", key, err)
	}
}

func TestALinkInPlaceOfTheKeyIsNotFollowed(t *testing.T) {
	profile := realPath(t.TempDir())
	target := filepath.Join(profile, "real")
	writeKey(t, target)
	link := filepath.Join(profile, "id_ed25519")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this account may not make a symbolic link: %v", err)
	}
	if _, err := readKeyFile(link, profile); !isLocalRefusal(err) {
		t.Errorf("%s, a link, was read as a key file (%v)", link, err)
	}
}
