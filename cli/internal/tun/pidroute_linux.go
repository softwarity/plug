//go:build linux

package tun

import (
	"os"
	"strconv"
	"strings"
)

// ppidOf reads /proc/<pid>/stat. Layout: "pid (comm) state ppid ...". comm may
// contain spaces AND ')', so we split on the LAST ')' and read from there: the
// remainder is "state ppid ...", so ppid is the 2nd field.
func ppidOf(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// pidForLocalPort finds the PID owning the TCP socket whose LOCAL port is srcPort:
// match /proc/net/tcp{,6} to get the socket inode, then scan /proc/<pid>/fd for a
// "socket:[inode]" symlink. The daemon runs as root, so it sees every process's
// fds. This is the Linux reference implementation of the connect-time lookup.
func pidForLocalPort(srcPort uint16) (int, bool) {
	inode, ok := inodeForLocalPort(srcPort)
	if !ok {
		return 0, false
	}
	return pidForSocketInode(inode)
}

// inodeForLocalPort scans /proc/net/tcp and /proc/net/tcp6 for a row whose local
// address ends in :<srcPort> (hex) and returns its socket inode.
func inodeForLocalPort(srcPort uint16) (string, bool) {
	want := strings.ToUpper(strconv.FormatUint(uint64(srcPort), 16))
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for _, ln := range lines[1:] { // skip header
			f := strings.Fields(ln)
			if len(f) < 10 {
				continue
			}
			// f[1] = "LOCALADDRHEX:PORTHEX"; compare the port part case-insensitively.
			if c := strings.LastIndexByte(f[1], ':'); c >= 0 && strings.EqualFold(f[1][c+1:], want) {
				return f[9], true // f[9] = inode
			}
		}
	}
	return "", false
}

// pidForSocketInode scans /proc/<pid>/fd for a symlink to socket:[inode].
func pidForSocketInode(inode string) (int, bool) {
	target := "socket:[" + inode + "]"
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid dir
		}
		fds, err := os.ReadDir("/proc/" + e.Name() + "/fd")
		if err != nil {
			continue // process gone or not ours
		}
		for _, fd := range fds {
			if link, err := os.Readlink("/proc/" + e.Name() + "/fd/" + fd.Name()); err == nil && link == target {
				return pid, true
			}
		}
	}
	return 0, false
}
