package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processUID is the real uid of pid, read from /proc/<pid>/status: the Uid
// line carries real, effective, saved and filesystem uids, and the real one is
// what the record's author would have had.
func processUID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, fmt.Errorf("PID %d: %v", pid, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				break
			}
			uid, err := strconv.Atoi(f[0])
			if err != nil {
				break
			}
			return uid, nil
		}
	}
	return 0, fmt.Errorf("PID %d: no Uid line in its status", pid)
}
