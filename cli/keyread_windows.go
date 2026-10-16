//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// readKeyFile reads a profile key, and decides whether it may on the file it
// actually opened.
//
// As text, one file has many names on Windows: a short 8.3 name for each
// component (C:\PROGRA~1, C:\Users\RUNNER~1) and any directory junction a user
// may create without privilege. A guard reading the text refused a key named
// in short form inside its owner's own profile, and let through a path that a
// junction in that profile sends into another one or into the system tree. A
// guard that resolved the path first and read it after still left the moment
// between the two, in which the junction can be swapped.
//
// So the key is opened once. Windows is asked what that handle is (its type,
// and its final path with every junction followed and every component in its
// long name), the two rules of keypathguard.go decide on that, and the key is
// read from the same handle. What was checked is what is read.
//
// ownerProfile is the profile the key must lie in (config.keyOwner), empty
// where there is none to enforce. The system tree is refused either way.
func readKeyFile(path, ownerProfile string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, refuse("%s is not a usable path (%v)", path, err)
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, refuse("%s is not a usable path (%v)", path, err)
	}
	// OPEN_REPARSE_POINT: the last component is opened as itself and never
	// followed, so a link there is seen as a link and refused below. The
	// junctions along the way are followed, and the final path says where to.
	// SQOS identification: should the name still reach a pipe, its server may
	// learn who is asking but cannot act as SYSTEM. Shared as os.ReadFile
	// shares, so a key another process holds open still reads.
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: abs, Err: err}
	}
	f := os.NewFile(uintptr(h), abs)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// A pipe, a device or a link has no final path worth asking for: the mode
	// rule refuses it on what it is.
	real := abs
	if fi.Mode().IsRegular() {
		if real, err = finalPath(h); err != nil {
			return nil, refuse("refusing to read %s as a key: Windows could not say which file it is (%v)", abs, err)
		}
	}
	roots := systemRootsForKeyGuard()
	for i, r := range roots {
		if r != "" {
			roots[i] = realPath(r)
		}
	}
	if why := keyPathRefusal(real, fi.Mode(), roots); why != "" {
		return nil, refuse("refusing to read %s as a key: %s", abs, why)
	}
	if ownerProfile != "" && keyOutsideOwnersProfile(real, realPath(ownerProfile)) {
		return nil, refuse("refusing to read %s as a key.\n"+
			"      The client that asked for it was registered by an account whose profile is %s,\n"+
			"      and that key is outside it. plug runs as a machine-wide service here, so reading\n"+
			"      a file from another account's profile would be doing for one user something they\n"+
			"      cannot do themselves.", abs, ownerProfile)
	}
	return io.ReadAll(f)
}

// realPath is the final path of a directory the key is measured against (the
// owner's profile, the system roots), which an administrator places and the
// user the guard constrains cannot move. A path that cannot be opened is kept
// as written.
func realPath(abs string) string {
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return abs
	}
	// No access asked, and BACKUP_SEMANTICS so that a directory opens.
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return abs
	}
	defer windows.CloseHandle(h)
	real, err := finalPath(h)
	if err != nil {
		return abs
	}
	return real
}

// finalPath is the path Windows gives the file behind h: every junction and
// link followed, every component in its long name. filepath.EvalSymlinks
// cannot say it, as it no longer follows junctions (Go 1.23).
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	// Flags 0: FILE_NAME_NORMALIZED, VOLUME_NAME_DOS, a drive-letter path.
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) >= len(buf) {
		return "", windows.ERROR_INSUFFICIENT_BUFFER
	}
	real := windows.UTF16ToString(buf[:n])
	// The answer comes in the \\?\ form; the rules compare plain paths.
	if rest, ok := strings.CutPrefix(real, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(real, `\\?\`), nil
}
