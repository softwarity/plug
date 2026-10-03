package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// replaceBinaryFile is the moment `plug update` swaps the launcher, and the
// privilege it carries (setuid root, CAP_SYS_ADMIN) is granted on the way. The
// order is the security property: the grant lands on the file we wrote, BEFORE
// it is moved into place, so there is never a window where a file somebody
// else put at the destination gets the privilege.

// The new binary is written beside the old one, the grant is handed the new
// file while the old one still answers at the target, and only then is the
// target replaced. Nothing temporary is left behind.
func TestReplaceBinaryGrantsTheNewFileBeforeMovingItIntoPlace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "plug")
	if err := os.WriteFile(target, []byte("old launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	granted := ""
	grant := func(path string) {
		granted = path
		if filepath.Dir(path) != dir {
			t.Errorf("granted %s, which is not beside the target: a rename across directories is a copy", path)
		}
		if path == target {
			t.Error("granted the TARGET path: that is the window this closes")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "new launcher" {
			t.Errorf("the granted file reads %q, %v; want the new bytes", got, err)
		}
		still, _ := os.ReadFile(target)
		if string(still) != "old launcher" {
			t.Errorf("the target already reads %q while the grant runs: the swap happened first", still)
		}
		if runtime.GOOS != "windows" {
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("the new file is %v at grant time, want 0755", fi.Mode().Perm())
			}
		}
	}

	if err := replaceBinaryFile(target, []byte("new launcher"), grant); err != nil {
		t.Fatalf("replaceBinaryFile: %v", err)
	}
	if granted == "" {
		t.Fatal("the grant was never called: the new launcher would have no privilege")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new launcher" {
		t.Errorf("the target reads %q, %v after the swap", got, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".plug-update-") {
			t.Errorf("a temporary %s was left beside the launcher", e.Name())
		}
	}
	if _, err := os.Stat(granted); err == nil && granted != target {
		t.Errorf("the granted file %s still exists apart from the target", granted)
	}
}

// Without a grant (nothing to re-grant) the swap still happens.
func TestReplaceBinaryWithoutAGrant(t *testing.T) {
	target := filepath.Join(t.TempDir(), "plug")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinaryFile(target, []byte("new"), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("target reads %q", got)
	}
}

// A directory that cannot be written leaves the old launcher exactly as it
// was: the failure is said, nothing is half-replaced.
func TestReplaceBinaryLeavesTheOldLauncherWhenItCannotWriteBesideIt(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory this account cannot write")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "plug")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := replaceBinaryFile(target, []byte("new"), func(string) { t.Error("granted a file that was never written") })
	if err == nil {
		t.Fatal("replaced a launcher in a directory this account cannot write")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("the old launcher reads %q after the failed update", got)
	}
}
