package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// profileDir is ~/.plug, opened once, for the commands that create, rewrite,
// rename or remove a profile: the launcher holds root on macOS while it does.
//
// The rule is the one guardUserPath states, applied to the DIRECTORY: plug as
// root only does what the user could have done without it. The directory is
// the user's, so the user may remove or replace any entry in it, a file owned
// by root included; what they may not do is write INTO a file that is not
// theirs. So plug never does either: a profile is rewritten as a new file,
// written under the user's ownership and renamed over the old entry, and
// removed by unlinking the entry. The old file is never opened for writing,
// which is what makes a hard link in ~/.plug to a system file harmless, and a
// symlink at the entry is replaced, never followed.
//
// The guard used to judge the FILE, and refused every profile a launcher up to
// 2.21.0 had written as root without handing it back: plug could no longer
// remove, rename or redefine its own files. Judging the directory keeps the
// rule and ends that.
//
// The ownership is checked on the DESCRIPTOR of the opened directory, and every
// operation goes through os.Root, relative to it: swapping ~/.plug for a symlink
// after the check changes nothing, plug works in the directory it checked. That
// closes the window a check on a path leaves before the write by that path.
type profileDir struct {
	root *os.Root
	name string // the directory's path, for messages
	// uid and gid are the account plug acts for; acting says plug holds a
	// privilege, so ownership is judged here rather than by the kernel.
	uid, gid int
	acting   bool
	// owner reads the owner and the link count out of a FileInfo; ok is
	// false where the platform has no such thing. A field so a test can stand
	// in the ownership a normal user cannot create (a root-owned file).
	owner func(fs.FileInfo) (uid int, links uint64, ok bool)
	// chown hands a freshly written file to the user, on its descriptor.
	chown func(f *os.File, uid, gid int) error
}

// openProfileDir opens ~/.plug for an edit. create makes the directory when it
// is missing, as the user's; without it a missing directory is an error the
// caller reads as "no such profile".
func openProfileDir(create bool) (*profileDir, error) {
	if create {
		if err := ensurePlugDir(); err != nil {
			return nil, err
		}
	}
	uid, gid, acting := dropTarget()
	return openProfileDirAs(plugDir(), uid, gid, acting, fileOwner, chownFile)
}

func openProfileDirAs(dir string, uid, gid int, acting bool,
	owner func(fs.FileInfo) (int, uint64, bool), chown func(*os.File, int, int) error) (*profileDir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	d := &profileDir{root: root, name: dir, uid: uid, gid: gid, acting: acting, owner: owner, chown: chown}
	if acting {
		fi, err := root.Stat(".")
		if err != nil {
			root.Close()
			return nil, err
		}
		if o, _, ok := owner(fi); ok && o != uid {
			root.Close()
			return nil, fmt.Errorf("refusing to edit profiles in %s as root: it is owned by uid %d, not by you (uid %d).\n"+
				"      plug runs setuid so it never has to ask for a password again; it will not use that\n"+
				"      privilege in a directory that is not yours. Check $HOME and any symlink under it", dir, o, uid)
		}
	}
	return d, nil
}

func (d *profileDir) Close() error { return d.root.Close() }

// isSymlink says the entry is a symbolic link, which the profile commands
// treat apart (writeProfileText): a profile kept as a dotfile elsewhere.
func (d *profileDir) isSymlink(file string) bool {
	fi, err := d.root.Lstat(file)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// read returns a profile's text and whether it exists. A file that is not the
// user's is read only when it has a single link: plug wrote it (a launcher up
// to 2.21.0 left its profiles to root), since a user can move no file of
// root's into their directory. A second link means the entry may be a hard
// link the user made to a file they cannot read, and its bytes would end up in
// the rewritten profile, theirs to read: refused.
func (d *profileDir) read(file string) (string, bool, error) {
	f, err := d.root.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", filepath.Join(d.name, file))
	}
	if d.acting {
		if o, links, ok := d.owner(fi); ok && o != d.uid && links != 1 {
			return "", false, fmt.Errorf("refusing to read %s as root: it is owned by uid %d and has %d links, so it may be a link to a file that is not yours.\n"+
				"      Remove it (rm %s) and define the profile again", filepath.Join(d.name, file), o, links, filepath.Join(d.name, file))
		}
	}
	// A profile is a few lines; the bound only keeps a mistaken file from
	// being read whole.
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// write replaces a profile with text: a new file, 0600, handed to the user on
// its descriptor, then renamed over the entry. The old file, whoever owns it,
// is never opened. A failure leaves the old profile as it was.
func (d *profileDir) write(file, text string) error {
	tmp := "." + file + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	f, err := d.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = d.root.Remove(tmp)
		}
	}()
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if d.acting {
		if err := d.chown(f, d.uid, d.gid); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := d.root.Rename(tmp, file); err != nil {
		return err
	}
	done = true
	return nil
}

// remove unlinks a profile's entry; a symlink goes, not what it points at.
func (d *profileDir) remove(file string) error { return d.root.Remove(file) }

// rename moves a profile's entry to a name that is free.
func (d *profileDir) rename(from, to string) error {
	if _, err := d.root.Lstat(from); err != nil {
		return err
	}
	if _, err := d.root.Lstat(to); err == nil {
		return fs.ErrExist
	}
	return d.root.Rename(from, to)
}

// ensurePlugDir creates ~/.plug when it is missing, as the user's. MkdirAll as
// root left it root's, and then nothing of plug's could go in: a first profile
// defined before any installer had made the directory locked the account out
// of its own ~/.plug. An existing directory is left alone: it is judged where
// it is opened.
func ensurePlugDir() error {
	dir := plugDir()
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := userPathError(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	chownToUser(dir)
	return nil
}

// profileFile is the entry name of a profile in ~/.plug, the name validated
// first, as profilePath does.
func profileFile(name string) string {
	if err := checkProfileName(name); err != nil {
		fatal("%v", err)
	}
	return name + ".conf"
}
