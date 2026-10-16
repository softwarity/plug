//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// realPath is a path as the file it opens, which is what the key guards
// compare. As text, one file has many names on Windows: a short 8.3 name for
// each component (C:\PROGRA~1, C:\Users\RUNNER~1) and any directory junction a
// user may create without privilege. A guard reading the text refused a key
// named in short form inside its owner's own profile, and let through a path
// that a junction in that profile sends into another one or into the system
// tree.
//
// So the file is opened, for no access at all, and Windows is asked what it
// opened: the final path of the handle, every junction and link followed and
// every component in its long name. filepath.EvalSymlinks cannot say it, as it
// no longer follows junctions (Go 1.23).
//
// A path that cannot be opened (absent, unreadable) is kept as written: the
// guards then decide on its text, as before, and the read that follows fails.
func realPath(abs string) string {
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return abs
	}
	// BACKUP_SEMANTICS so a directory opens too: the guards resolve the
	// profile and the system roots the same way as the key.
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return abs
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	// Flags 0: FILE_NAME_NORMALIZED, VOLUME_NAME_DOS, a drive-letter path.
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || n == 0 || int(n) >= len(buf) {
		return abs
	}
	real := windows.UTF16ToString(buf[:n])
	// The answer comes in the \\?\ form; the guards compare plain paths.
	if rest, ok := strings.CutPrefix(real, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(real, `\\?\`)
}
