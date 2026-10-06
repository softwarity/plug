package main

// The profile directory (profilestore.go): plug edits profiles by replacing the
// entry in a directory proven the user's, never by writing into the file. A
// test process is unprivileged, so the ownership plug judges as root is stood
// in through profileDir.owner: a normal user cannot make a file root's, but
// can make the comparison plug makes for real see one.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mine opens dir as plug unprivileged would: the kernel judges, nothing stood in.
func mine(t *testing.T, dir string) *profileDir {
	t.Helper()
	d, err := openProfileDirAs(dir, os.Getuid(), os.Getgid(), false, fileOwner, chownFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// asRoot opens dir as the setuid launcher would, for the user `me`, with the
// directory the user's and every file in it root's: what a launcher up to
// 2.21.0 left behind. chowned records what plug handed back.
func asRoot(t *testing.T, dir string, chowned *[]int) *profileDir {
	t.Helper()
	const me = 501
	owner := func(fi fs.FileInfo) (int, uint64, bool) {
		_, links, ok := fileOwner(fi)
		if !ok {
			links = 1
		}
		if fi.IsDir() {
			return me, links, true
		}
		return 0, links, true
	}
	chown := func(_ *os.File, uid, gid int) error {
		*chowned = append(*chowned, uid, gid)
		return nil
	}
	d, err := openProfileDirAs(dir, me, 20, true, owner, chown)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The case that locked plug out of its own profiles: a root-owned profile in
// the user's ~/.plug. It is read, rewritten as a new file handed to the user,
// renamed and removed. Would have caught: the guard judging the FILE, which
// refused all four.
func TestRootOwnedProfileIsManagedLikeAnyOther(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "canopy.conf"), "host = localhost\nport = 32222\n")
	var chowned []int
	d := asRoot(t, dir, &chowned)

	text, exists, err := d.read("canopy.conf")
	if err != nil || !exists || !strings.Contains(text, "port = 32222") {
		t.Fatalf("a profile plug wrote as root must stay readable: %q %v %v", text, exists, err)
	}
	if err := d.write("canopy.conf", upsertProfileKeys(text, [2]string{"port", "2222"})); err != nil {
		t.Fatalf("rewriting a root-owned profile: %v", err)
	}
	if got := readText(t, filepath.Join(dir, "canopy.conf")); !strings.Contains(got, "port = 2222") {
		t.Fatalf("rewritten profile: %q", got)
	}
	if len(chowned) != 2 || chowned[0] != 501 || chowned[1] != 20 {
		t.Fatalf("the new file must be handed to the user, got chown calls %v", chowned)
	}
	if err := d.rename("canopy.conf", "kind.conf"); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if err := d.remove("kind.conf"); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

// A hard link in ~/.plug to a file that is not the user's: the attack the July
// guard was written against, in its other shape. plug must neither write into
// it nor carry its bytes into a profile the user can read. Would have caught:
// a rewrite in place (the target's bytes replaced), or a read of a foreign
// file with several links (the target's bytes copied into the new profile).
func TestHardLinkToAForeignFileIsNeitherWrittenNorRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no setuid launcher on Windows")
	}
	dir, elsewhere := t.TempDir(), t.TempDir()
	victim := filepath.Join(elsewhere, "secret")
	write(t, victim, "SECRET\n")
	if err := os.Link(victim, filepath.Join(dir, "prod.conf")); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	var chowned []int
	d := asRoot(t, dir, &chowned)

	if _, _, err := d.read("prod.conf"); err == nil {
		t.Fatal("a root-owned file with two links was read: its bytes would land in the user's profile")
	}
	// Defining the profile again replaces the entry and never touches the target.
	if err := d.write("prod.conf", "host = h\nport = 2222\n"); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, victim); got != "SECRET\n" {
		t.Fatalf("the linked file was written into: %q", got)
	}
	if got := readText(t, filepath.Join(dir, "prod.conf")); got != "host = h\nport = 2222\n" {
		t.Fatalf("the profile: %q", got)
	}
}

// A symlink at the entry: removed as a link, replaced as a link, its target
// untouched. Would have caught: Remove or a write following the link out of
// the directory.
func TestSymlinkEntryIsReplacedNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	dir, elsewhere := t.TempDir(), t.TempDir()
	target := filepath.Join(elsewhere, "target")
	write(t, target, "untouched\n")
	link := filepath.Join(dir, "x.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	d := mine(t, dir)
	if !d.isSymlink("x.conf") {
		t.Fatal("a symlink entry must be seen as one (editProfile writes through it)")
	}
	if err := d.remove("x.conf"); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, target); got != "untouched\n" {
		t.Fatalf("removing the link touched its target: %q", got)
	}
}

// A directory that is not the user's is refused before anything is done in it:
// ~/.plug pointed at a system directory is the July attack itself.
func TestForeignDirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	owner := func(fs.FileInfo) (int, uint64, bool) { return 0, 1, true }
	_, err := openProfileDirAs(dir, 501, 20, true, owner, chownFile)
	if err == nil || !strings.Contains(err.Error(), "not by you") {
		t.Fatalf("a root-owned directory must be refused, got %v", err)
	}
}

// The check and the work are on the same directory: swapping the path for a
// symlink to another directory once it is open changes nothing. Would have
// caught: an edit that resolves the path again after the check.
func TestTheCheckedDirectoryIsTheOneWrittenIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	base := t.TempDir()
	dir, moved, other := filepath.Join(base, "plug"), filepath.Join(base, "moved"), filepath.Join(base, "other")
	for _, p := range []string{dir, other} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := mine(t, dir)
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if err := d.write("p.conf", "host = h\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "p.conf")); err == nil {
		t.Fatal("the write followed the swapped path into another directory")
	}
	if got := readText(t, filepath.Join(moved, "p.conf")); got != "host = h\n" {
		t.Fatalf("the checked directory did not get the profile: %q", got)
	}
}

// A write leaves the profile and nothing else: no temporary file that `plug ls`
// would list or a later write would trip on. And renaming onto a profile that
// exists is refused, not a silent overwrite.
func TestWriteLeavesNoTemporaryAndRenameKeepsAnExistingProfile(t *testing.T) {
	dir := t.TempDir()
	d := mine(t, dir)
	for i := 0; i < 3; i++ {
		if err := d.write("a.conf", "host = a\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "b.conf"), "host = b\n")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected a.conf and b.conf only, got %v", entries)
	}
	if err := d.rename("a.conf", "b.conf"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renaming onto an existing profile: %v", err)
	}
	if got := readText(t, filepath.Join(dir, "b.conf")); got != "host = b\n" {
		t.Fatalf("b.conf was overwritten: %q", got)
	}
}

// The commands end to end, unprivileged: a profile defined, edited by a key,
// renamed and removed through the functions the CLI calls. And ~/.plug made
// when missing, private.
func TestProfileCommandsThroughTheStore(t *testing.T) {
	sandboxHome(t)
	writeProfile("prod", "h.example", "2222")
	fi, err := os.Stat(plugDir())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Fatalf("~/.plug made %v, want 0700", fi.Mode().Perm())
	}
	setProfileKey("prod", "update", "none")
	if got := readText(t, profilePath("prod")); got != "host = h.example\nport = 2222\nupdate = none\n" {
		t.Fatalf("profile: %q", got)
	}
	cmdRenameProfile([]string{"prod", "staging"})
	if _, err := os.Stat(profilePath("staging")); err != nil {
		t.Fatal(err)
	}
	cmdRemoveProfile([]string{"staging"})
	if names := listProfiles(); len(names) != 0 {
		t.Fatalf("profiles left: %v", names)
	}
}

// A profile kept as a dotfile elsewhere is still edited THROUGH its link: the
// link stays a link, the dotfile gets the change.
func TestDotfileProfileIsWrittenThroughItsLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	sandboxHome(t)
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	dotfile := filepath.Join(t.TempDir(), "prod.conf")
	write(t, dotfile, "# mine\nhost = old\nport = 2222\n")
	if err := os.Symlink(dotfile, profilePath("prod")); err != nil {
		t.Fatal(err)
	}
	writeProfile("prod", "new", "2222")
	if fi, err := os.Lstat(profilePath("prod")); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the dotfile link was replaced by a copy: %v %v", fi, err)
	}
	if got := readText(t, dotfile); got != "# mine\nhost = new\nport = 2222\n" {
		t.Fatalf("dotfile: %q", got)
	}
}
