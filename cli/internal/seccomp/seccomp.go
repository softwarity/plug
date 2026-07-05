// Package seccomp wires plug's Go / statically-linked coverage layer on Linux.
//
// The LD_PRELOAD hook (package inject) only reaches libc runtimes. Go bypasses
// libc for both name resolution (its pure-Go resolver) and the connection (a raw
// connect(2) syscall), so a Go binary never hits the hook. This package embeds a
// small native supervisor (csrc/plug_seccomp.c) that traps the child's
// connect(2) with a rootless seccomp user-notifier and reroutes cluster
// connections through the same SOCKS proxy — closing the gap at the kernel
// boundary. See the C source for the mechanism.
//
// plug wraps the child command with the supervisor:
//
//	plug-seccomp <cmd> [args...]
//
// passing PLUG_SOCKS (the proxy) and PLUG_PRELOAD (the hook to re-inject into the
// child, NOT the supervisor). The supervisor degrades to a plain exec wrapper
// wherever seccomp is denied, so wrapping is always safe.
package seccomp

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// superasset holds the compiled supervisor, named plug-seccomp-linux-<arch>.
// The directory always ships at least a README so this embed never fails on a
// build where no binary was placed (e.g. a plain macOS `go build`).
//
//go:embed all:superasset
var superasset embed.FS

// EnvVarDisable turns the supervisor off when set to a non-empty, non-"0" value.
const EnvVarDisable = "PLUG_NO_SECCOMP"

// EnvVarPreload carries the LD_PRELOAD value the supervisor re-injects into the
// child (kept off the supervisor's own env so its resolver/SOCKS calls stay
// real). The C side reads it.
const EnvVarPreload = "PLUG_PRELOAD"

// assetName is the embedded supervisor file for the current platform, or "" if
// this platform has no supervisor (everything but Linux).
func assetName() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return fmt.Sprintf("superasset/plug-seccomp-linux-%s", runtime.GOARCH)
}

// Disabled reports whether the user asked to skip the supervisor.
func Disabled() bool {
	v := os.Getenv(EnvVarDisable)
	return v != "" && v != "0"
}

// Available reports whether an embedded supervisor exists for this platform and
// the user has not disabled it.
func Available() bool {
	if Disabled() {
		return false
	}
	name := assetName()
	if name == "" {
		return false
	}
	f, err := superasset.Open(name)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// libDir is where plug writes the extracted supervisor (~/.plug/lib), shared
// with the extracted hook library.
func libDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".plug", "lib"), nil
}

// Prepare extracts the supervisor for this platform to ~/.plug/lib and returns
// its path. The file name carries a content hash so a plug upgrade lands at a
// new path (no stale reuse). Returns ok=false — with a note via logf — when
// unavailable or on any extraction error, so the caller falls back cleanly.
func Prepare(logf func(string, ...any)) (string, bool) {
	note := func(format string, a ...any) {
		if logf != nil {
			logf(format, a...)
		}
	}
	if !Available() {
		return "", false
	}
	name := assetName()
	data, err := fs.ReadFile(superasset, name)
	if err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	dir, err := libDir()
	if err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	sum := sha256.Sum256(data)
	dst := filepath.Join(dir, fmt.Sprintf("plug-seccomp-%s", hex.EncodeToString(sum[:8])))
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(data)) {
		return dst, true
	}
	tmp, err := os.CreateTemp(dir, ".plug-seccomp-*")
	if err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	if err := tmp.Close(); err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		note("seccomp supervisor off: %v", err)
		return "", false
	}
	return dst, true
}
