package main

import "testing"

// What mintty names its pipes, against what a CI runner, a shell pipeline or a
// redirect gives a program. Only the first kind has a person on the other end.
func TestGitBashsTerminalIsToldFromAPipe(t *testing.T) {
	for name, want := range map[string]bool{
		`\msys-dd50a72ab4668b33-pty0-from-master`:                  true,
		`\cygwin-e022582115c10879-pty3-from-master`:                true,
		`\Device\NamedPipe\msys-dd50a72ab4668b33-pty1-from-master`: true,
		`\msys-dd50a72ab4668b33-pty0-to-master`:                    false, // the output side
		`\msys-dd50a72ab4668b33-13752-pipe-nt-0x1`:                 false, // `a | b` in bash
		`\Win32Pipes.0000163c.00000002`:                            false, // an anonymous pipe
		`\msys--pty0-from-master`:                                  false,
		``:                                                         false,
	} {
		if got := isMsysPty(name); got != want {
			t.Errorf("isMsysPty(%q) = %v, want %v", name, got, want)
		}
	}
}
