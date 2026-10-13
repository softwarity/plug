//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// minttyInput is the terminal to ask a question on when stdin is mintty's
// pseudo-terminal (see isMsysPty), nil otherwise. A copy of the handle, so the
// caller closing what it asked on leaves plug's stdin open for the command.
func minttyInput() *os.File {
	h := windows.Handle(os.Stdin.Fd())
	if t, err := windows.GetFileType(h); err != nil || t != windows.FILE_TYPE_PIPE {
		return nil
	}
	// FILE_NAME_INFO: the length in bytes, then the name in UTF-16.
	var buf [4 + windows.MAX_PATH*2]byte
	if windows.GetFileInformationByHandleEx(h, windows.FileNameInfo, &buf[0], uint32(len(buf))) != nil {
		return nil
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0])) / 2
	if n == 0 || int(n) > windows.MAX_PATH {
		return nil
	}
	name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), n))
	if !isMsysPty(name) {
		return nil
	}
	self := windows.CurrentProcess()
	var dup windows.Handle
	if windows.DuplicateHandle(self, h, self, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		return nil
	}
	return os.NewFile(uintptr(dup), "mintty")
}
