package main

// A session that serves a name records itself under ~/.plug/served, so the NEXT
// session — the one the agent refuses because the name is taken — can say WHO
// holds it, and offer to stop it.
//
// The agent is the authority on whether a name is held: it answers from the
// lease, and a lease only refuses while the holder's forward still answers. What
// the agent cannot know is WHICH local process that is — and that is the whole
// problem, because closing an editor takes its terminal panes away without
// killing what ran in them. The holder is then alive, invisible, and reachable
// by no window: the state that reads as a "ghost session".
//
// Best-effort throughout: a session must never fail to start because this could
// not be written.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func servedDir() string { return filepath.Join(plugDir(), "served") }

// servedRecord is what a session leaves behind about the name it serves.
type servedRecord struct {
	pid  int
	port string // the agent-side port it holds the name on — its identity
	dir  string
	cmd  string
	age  time.Duration
}

// markServed records this process as the one serving name on agentPort, and
// returns the cleanup that forgets it.
func markServed(name, agentPort string, cmdArgs []string) func() {
	path := filepath.Join(servedDir(), name) // name is a validated DNS label
	guardUserPath(path)
	if os.MkdirAll(servedDir(), 0o700) != nil {
		return func() {}
	}
	cwd, _ := os.Getwd()
	body := fmt.Sprintf("pid = %d\nport = %s\ndir = %s\ncmd = %s\n",
		os.Getpid(), agentPort, cwd, strings.Join(cmdArgs, " "))
	if os.WriteFile(path, []byte(body), 0o600) != nil {
		return func() {}
	}
	// Written as euid 0 by the setuid helper — hand it back, like the core cache.
	chownToUser(servedDir())
	chownToUser(path)
	return func() { _ = os.Remove(path) }
}

// servedHolder reads what this machine recorded about name, or nil when there is
// nothing to say: no record, or a record whose process is gone. A leftover
// record is worse than silence — PIDs get reused, and what we do with this is
// offer to kill it.
func servedHolder(name string) *servedRecord {
	data, err := os.ReadFile(filepath.Join(servedDir(), name))
	if err != nil {
		return nil
	}
	r := &servedRecord{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k, v = strings.TrimSpace(k), strings.TrimSpace(v); k {
		case "pid":
			r.pid, _ = strconv.Atoi(v)
		case "port":
			r.port = v
		case "dir":
			r.dir = v
		case "cmd":
			r.cmd = v
		}
	}
	if r.pid <= 0 || !processAlive(r.pid) {
		return nil
	}
	if fi, serr := os.Stat(filepath.Join(servedDir(), name)); serr == nil {
		r.age = time.Since(fi.ModTime())
	}
	return r
}

// describe renders a record for someone deciding whether to stop it.
func (r *servedRecord) describe() string {
	out := fmt.Sprintf("PID %d, started %s", r.pid, roughAge(r.age))
	if r.dir != "" {
		out += "\n        dir: " + r.dir
	}
	if r.cmd != "" {
		out += "\n        cmd: " + r.cmd
	}
	return out
}

// heldPortRe pulls the holder's agent port out of the agent's refusal. It is
// what proves a local record IS the holder rather than a leftover naming a
// recycled PID — without it, "stop it?" could point at an innocent process.
func heldPort(msg string) string {
	const marker = "agent port "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	end := strings.IndexFunc(rest, func(c rune) bool { return c < '0' || c > '9' })
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// holderIsOurs reports whether the session the agent refused us for is the one
// this machine recorded — the check that makes stopping it safe to OFFER.
//
// Identity is the agent port: the refusal names the port the holder serves on,
// and the record names the port we served on. Equal means it IS that session.
// Without this a stale record — a session killed with -9, never cleaned up —
// would put an innocent process behind the question, since the OS reuses PIDs.
// An agent too old to name a port simply never gets the offer.
func holderIsOurs(r *servedRecord, refusal string) bool {
	return r != nil && r.port != "" && r.port == heldPort(refusal)
}

// askToStop asks whether to stop the holder. No terminal — a script, a CI job,
// a detached run — means no question and no kill: a prompt nobody can answer
// blocks the session for ever, which is far worse than the refusal it replaces.
//
// Being able to OPEN the terminal device is not the test. On Windows, CONIN$
// opens quite happily in a CI job with no console attached, and the read that
// follows never returns — 16 minutes of a Windows e2e leg went that way before
// this was written. Ask the OS whether stdin IS a terminal, which is the real
// question, and keep a backstop deadline for any context neither of us thought
// of: an unanswered question falls back to reporting, never to killing.
func askToStop(r *servedRecord) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	tty, err := os.Open(ttyDevice)
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(os.Stderr, "[plug] that name is served by another session of yours:\n        %s\n", r.describe())
	fmt.Fprint(os.Stderr, "[plug] stop it and take the name? [Y/n]: ")
	answer := make(chan string, 1)
	go func() {
		line, rerr := bufio.NewReader(tty).ReadString('\n')
		if rerr != nil {
			line = "n"
		}
		answer <- strings.ToLower(strings.TrimSpace(line))
	}()
	select {
	case a := <-answer:
		return a == "" || a == "y" || a == "yes" || a == "o" || a == "oui"
	case <-time.After(askToStopDeadline):
		fmt.Fprintln(os.Stderr, "\n[plug] no answer — leaving that session alone")
		return false
	}
}

// askToStopDeadline is long enough that someone reading the command line before
// deciding is never cut off, and short enough that a context we mistook for
// interactive cannot wedge a session.
const askToStopDeadline = 2 * time.Minute

// stopHolder asks the holder to stop and waits for it to let the name go.
//
// SIGTERM, never SIGKILL: plug relays it to the command, then runs its teardown
// — which is what releases the name AND restores whatever the session parked. A
// killed session would leave both behind.
func stopHolder(r *servedRecord) error {
	p, err := os.FindProcess(r.pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	// Its teardown has a cluster round-trip to make; wait for the process to go
	// rather than for a fixed delay.
	for i := 0; i < 100; i++ {
		if !processAlive(r.pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("PID %d is still running 10s after being asked to stop", r.pid)
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
