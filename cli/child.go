package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// userPath is $PATH exactly as the human set it, captured before securePath
// narrows the one plug uses for its own privileged helper lookups. Your command
// — and anything it spawns — is resolved and run with THIS one.
var userPath = os.Getenv("PATH")

// pathNarrowed records that securePath actually replaced $PATH this run.
// False on every unprivileged invocation and always on Windows (no setuid;
// euid == ruid there) — in those cases the process $PATH IS the human's and
// there is nothing to restore.
var pathNarrowed bool

// securePath narrows $PATH to root-owned system directories while plug holds a
// privilege the caller does not.
//
// plug is setuid root on macOS and carries ambient caps on Linux, on purpose:
// root once at install, never a prompt afterwards. But it then drives system
// helpers by BARE NAME — ip, ifconfig, route, scutil, sudo, netstat… — resolved
// through the caller's $PATH. A `PATH=/tmp/evil:$PATH` with a fake `ip` is
// therefore a fake `ip` running as root (macOS) or with CAP_SYS_ADMIN (Linux,
// which withPrivCaps hands to the child explicitly).
//
// The list is not a hardcoded /sbin/ip: NixOS has no /sbin at all, and pinning
// one layout would break those machines. These are the root-owned directories
// where system tooling lives across mainstream distros, macOS and NixOS.
//
// Unprivileged, this is a no-op: the caller's $PATH is their own business, and
// narrowing it would break the very setups (Nix, Homebrew, asdf) plug should
// stay out of the way of.
func securePath() {
	if euid, ruid := os.Geteuid(), os.Getuid(); euid == ruid {
		return
	}
	pathNarrowed = true
	_ = os.Setenv("PATH", securePathList)
}

// securePathList is the $PATH plug gives itself while privileged. Named because
// the core also COMPARES against it: see runChildEnv.
const securePathList = "/usr/sbin:/usr/bin:/sbin:/bin:/run/current-system/sw/bin:/run/wrappers/bin"

// withUserPath returns env with $PATH forced back to the human's — plug's
// narrowed one must never leak into the command it launches.
func withUserPath(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+userPath)
}

// lookPathIn resolves file against an explicit path list instead of the
// process's $PATH — how the child's command is found with the human's PATH
// while plug's own is narrowed.
func lookPathIn(file, path string) (string, error) {
	if strings.ContainsRune(file, os.PathSeparator) {
		return file, nil // already a path: exec.Command handles it
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, file)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", file)
}

// runChildEnv runs the command, optionally with an explicit environment
// (nil = inherit the current one).
func runChildEnv(cmdArgs []string, env []string) int {
	// YOUR command is resolved and run with YOUR $PATH — plug narrows its own
	// only for the system helpers it drives as root (see securePath).
	child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if pathNarrowed {
		if p, err := lookPathIn(cmdArgs[0], userPath); err == nil {
			child.Path = p
			// exec.Command already failed to resolve the name via the narrowed
			// $PATH and parked that error in child.Err — which Start() would
			// return even with Path corrected. This resolution supersedes it.
			child.Err = nil
		}
		env = withUserPath(env)
	}
	child.Env = env
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	// When plug is the macOS setuid-root helper, this process runs with euid 0 so
	// it can hold the utun + DNS — but YOUR command must not. Drop the child back to
	// the human user (no-op on the Linux caps path). See privdrop_unix.go.
	applyPrivDrop(child)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	if err := child.Start(); err != nil {
		// A core is exec'd by the launcher and captures $PATH at startup as the
		// human's. A launcher older than this core narrowed it FIRST and handed
		// that over, so what we hold is the narrow list itself — no human's
		// $PATH has ever looked like that, and the real one is unrecoverable
		// here. Name the skew instead of blaming a command that IS installed.
		if pathNarrowed && userPath == securePathList {
			fatal("cannot start %q: it is not in the system directories, and this core was handed a\n"+
				"      narrowed $PATH by a launcher older than itself — your own $PATH never reached it.\n"+
				"      Reinstall the launcher from the agent to pair them:  plug update", cmdArgs[0])
		}
		info("cannot start %q: %v", cmdArgs[0], err)
		return 127
	}
	go func() {
		for s := range sigs {
			// A terminal Ctrl-C is delivered by the kernel to the WHOLE
			// foreground process group — the child included (it shares ours).
			// SIGINT is caught here only so plug survives to run its teardown;
			// re-sending it made every Ctrl-C a DOUBLE SIGINT for the child,
			// and dev servers treat the second as "force quit NOW" — dying
			// without restoring the terminal they put in raw mode (arrow keys
			// then echo ^[[A in the shell). A targeted SIGTERM at plug alone
			// is NOT group-delivered, so that one is relayed.
			//
			// No nil guard needed here, unlike its twin in execCore: this
			// goroutine starts after child.Start() has returned, so the process
			// exists before anything can signal it.
			if s == syscall.SIGTERM {
				child.Process.Signal(s)
			}
		}
	}()

	err := child.Wait()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
