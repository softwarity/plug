//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A --mount lands where a privilege the user does not hold makes it land: root
// creates the directory on macOS, CAP_SYS_ADMIN mounts over it on Linux. So the
// point must be one the user could have made themselves, and the walk judges
// the deepest component that exists, exactly as the write guard does.
func TestAMountpointMustBelongToTheUser(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "home")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatal(err)
	}

	// Under a directory the user owns, whether or not the leaf exists yet: this
	// is the explicit --mount under the home, and the automatic mount under the
	// session directory plug created and handed to the user.
	for _, p := range []string{mine, filepath.Join(mine, "mnt", "data")} {
		if err := mountpointOwnedBy(p, os.Getuid()); err != nil {
			t.Errorf("a mountpoint under the user's own tree was refused: %v", err)
		}
	}

	// The same paths judged for somebody else: refused, and the refusal names
	// the component that decided it, so the person can tell which link under
	// their path sent the mount out of their tree.
	err := mountpointOwnedBy(filepath.Join(mine, "mnt", "data"), stranger())
	if err == nil {
		t.Fatal("a mountpoint whose nearest existing ancestor belongs to another uid was allowed: root would create it there")
	}
	if !strings.Contains(err.Error(), "resolves to "+mine+",") || !strings.Contains(err.Error(), "refusing to mount") {
		t.Errorf("the refusal does not name the ancestor that decided it, or is not about a mount:\n%s", err)
	}

	// An existing leaf owned by somebody else is refused on its own account.
	if err := mountpointOwnedBy(mine, stranger()); err == nil {
		t.Error("an existing directory owned by another uid was accepted as a mountpoint")
	}
}

// ensureMountpoint asks the ownership question BEFORE it creates anything: the
// creation runs with privilege, so a refusal that came after it would be a
// refusal of a directory root had already made.
func TestEnsureMountpointAsksBeforeItCreates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnt", "data")
	if err := ensureMountpoint(path); err != nil {
		t.Fatalf("a mountpoint under the user's own temp directory was refused: %v", err)
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		t.Fatalf("ensureMountpoint did not create %s: %v", path, err)
	}
	// A file in the way is refused, and left alone.
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureMountpoint(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a regular file was accepted as a mountpoint: %v", err)
	}
}
