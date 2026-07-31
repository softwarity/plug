package main

// A session that serves a name records itself under ~/.plug/served, so that the
// NEXT session — the one the agent refuses because the name is taken — can say
// WHO holds it instead of leaving you to hunt.
//
// The agent is the authority on whether a name is held: it answers from the
// lease, and a lease only refuses while the holder's forward still answers. So
// a refusal is already proof that some session is alive. What the agent cannot
// know is WHICH local process that is — and that is the whole problem, because
// closing an editor takes its terminal panes away without killing what ran in
// them. The holder is then alive, invisible, and reachable by no window: the
// state that reads as a "ghost session".
//
// Best-effort throughout. A session must never fail to start because this could
// not be written, and a record is only ever a HINT: it carries the directory and
// command line so a human confirms it is the right process before killing it —
// PIDs get reused, records can go stale.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func servedDir() string { return filepath.Join(plugDir(), "served") }

// markServed records this process as the one serving name, and returns the
// cleanup that forgets it.
func markServed(name string, cmdArgs []string) func() {
	path := filepath.Join(servedDir(), name) // name is a validated DNS label
	guardUserPath(path)
	if os.MkdirAll(servedDir(), 0o700) != nil {
		return func() {}
	}
	cwd, _ := os.Getwd()
	body := fmt.Sprintf("pid = %d\ndir = %s\ncmd = %s\n",
		os.Getpid(), cwd, strings.Join(cmdArgs, " "))
	if os.WriteFile(path, []byte(body), 0o600) != nil {
		return func() {}
	}
	// Written as euid 0 by the setuid helper — hand it back, like the core cache.
	chownToUser(servedDir())
	chownToUser(path)
	return func() { _ = os.Remove(path) }
}

// servedHolder describes the live local process recorded as serving name, or ""
// when there is nothing useful to say (no record, or the process is gone — a
// record whose process died is a leftover, and pointing at it would be worse
// than silence).
func servedHolder(name string) string {
	path := filepath.Join(servedDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pid int
	var dir, cmd string
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "pid":
			pid, _ = strconv.Atoi(v)
		case "dir":
			dir = v
		case "cmd":
			cmd = v
		}
	}
	if pid <= 0 || !processAlive(pid) {
		return ""
	}
	since := ""
	if fi, serr := os.Stat(path); serr == nil {
		since = ", started " + roughAge(time.Since(fi.ModTime()))
	}
	out := fmt.Sprintf("held on this machine by PID %d%s", pid, since)
	if dir != "" {
		out += "\n        dir: " + dir
	}
	if cmd != "" {
		out += "\n        cmd: " + cmd
	}
	return out + fmt.Sprintf("\n      Check it is yours, then free the name with:  kill %d", pid)
}

// roughAge renders a duration the way someone reads it off a screen.
func roughAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm ago", int(d.Hours()), int(d.Minutes())%60)
	}
}
