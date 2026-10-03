//go:build windows

package tunnel

import "os"

// The platform half of the known_hosts guard (see openKnownHosts and
// writeKnownHosts in transport.go). Windows has no uid to compare, so the
// owner is never checked here: the service writes the machine-wide file in
// %ProgramData%\plug as LocalSystem, which the installer made user-writable
// on purpose. What still holds is that a link planted at that path is never
// followed: openKnownHosts refuses anything but a regular file before and
// after the open, and writeKnownHosts lands by rename, which replaces a link
// rather than writing through it.

// noFollow has no Windows flag; the Lstat and the fstat around the open are
// the check.
const noFollow = 0

// fileOwner reports no owner: there is no uid to compare it with.
func fileOwner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// chownTo is a no-op: with no owner checked, there is nobody to hand the file to.
func chownTo(string, int, int) error { return nil }
