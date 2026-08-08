package main

import "os"

// execTarget names the verified core in a way nothing can substitute.
//
// plug checks the cached core against the digest the agent serves, then runs it
// with the privilege plug holds — ambient capabilities here, root on macOS.
// Checking a PATH and then executing that PATH leaves a gap between the two,
// and whatever can write into the cache during that gap runs with that
// privilege. What can write there is anything running as the user: the
// postinstall of the very project plug is launching, for one.
//
// A descriptor is bound to an inode. /proc/self/fd/N resolves to the file the
// descriptor holds, so a binary swapped in at that path afterwards is a
// different file and never the one that runs. The descriptor is handed to the
// child as its fd 3 (ExtraFiles), which is why the number is fixed here.
//
// This is what fexecve does on Linux when execveat is not available, and it
// costs nothing: no extra privilege, no change of layout, no second read.
func execTarget(f *os.File) (string, []*os.File) {
	return "/proc/self/fd/3", []*os.File{f}
}
