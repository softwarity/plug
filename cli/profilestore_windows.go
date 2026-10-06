package main

import (
	"io/fs"
	"os"
)

// Windows never runs the launcher with a privilege over the user's files (see
// privdrop_windows.go): the kernel judges every profile edit, and there is
// nothing to hand back.

func dropTarget() (uid, gid int, ok bool) { return 0, 0, false }

func fileOwner(fs.FileInfo) (uid int, links uint64, ok bool) { return 0, 0, false }

func chownFile(*os.File, int, int) error { return nil }
