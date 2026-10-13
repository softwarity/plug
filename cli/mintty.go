package main

import "strings"

// isMsysPty reports whether a pipe's name is the input side of an MSYS or
// Cygwin pseudo-terminal, which is what a Windows program's stdin is under
// mintty, the terminal of Git Bash: `\msys-<hex>-pty<n>-from-master`.
//
// A native program sees no console there, only that pipe, so every check for a
// terminal says no and plug used to ask nothing at all in the window where most
// Windows developers type it. The line discipline lives on the terminal's side
// of the pipe (echo, Enter, backspace), so a line read from it is exactly what
// the person typed, which is all a question needs.
func isMsysPty(name string) bool {
	name = strings.TrimPrefix(name, `\Device\NamedPipe`)
	f := strings.Split(name, "-")
	if len(f) < 5 || (f[0] != `\msys` && f[0] != `\cygwin`) || f[1] == "" {
		return false
	}
	return strings.HasPrefix(f[2], "pty") && f[3] == "from" && f[4] == "master"
}
