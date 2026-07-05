// Package inject wires plug's transparent connect()/DNS interception layer (the
// "N1" hook) into the child process.
//
// The hook is a small native shared library (plug_hook.c, built by the Makefile
// in this directory) that intercepts connect() and getaddrinfo() in a libc-based
// child and routes every outbound TCP connection through plug's local SOCKS5
// proxy — no per-service forward needed. This package embeds the compiled
// library, writes it to ~/.plug/lib/ on first use, and produces the environment
// entries that load it into the child:
//
//	DYLD_INSERT_LIBRARIES=<lib>   (macOS)   /   LD_PRELOAD=<lib>   (Linux)
//	PLUG_SOCKS=<host:port>        (the SOCKS proxy the hook connects through)
//
// Injection is an ADDITIONAL layer on top of the existing proxy env and
// port-forwards, never a replacement. It is a no-op (and Env returns nil) when
// disabled via $PLUG_NO_INJECT, when the running OS has no embedded library, or
// when extraction fails — plug keeps working through the env-proxy in every such
// case.
package inject

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

// libassets holds the compiled hook libraries, named
// plug-hook-<goos>-<goarch>.{dylib,so}. The directory always contains at least a
// README so this embed never fails, even on a build where no library was placed
// (e.g. the Linux agent image, which cannot cross-build the macOS .dylib).
//
//go:embed all:libassets
var libassets embed.FS

// EnvVarDisable turns injection off when set to a non-empty, non-"0" value.
const EnvVarDisable = "PLUG_NO_INJECT"

// EnvVarSocks names the SOCKS proxy the hook connects through. The hook reads it;
// plug sets it here.
const EnvVarSocks = "PLUG_SOCKS"

// assetName is the embedded file name for the current platform, or "" if this OS
// is not injectable.
func assetName() string {
	switch runtime.GOOS {
	case "darwin":
		// One universal (arm64+amd64) fat dylib covers both Macs — and the Linux
		// agent image can't build a .dylib, so it ships committed to the repo.
		return "libassets/plug-hook-darwin.dylib"
	case "linux":
		// Per-arch ELF shared objects, built into libassets by the agent
		// Dockerfile (ELF has no fat format).
		return fmt.Sprintf("libassets/plug-hook-linux-%s.so", runtime.GOARCH)
	default:
		return "" // Windows etc. — no libc injection
	}
}

// PreloadVar is the loader environment variable for the current OS.
func PreloadVar() string {
	if runtime.GOOS == "darwin" {
		return "DYLD_INSERT_LIBRARIES"
	}
	return "LD_PRELOAD"
}

// Disabled reports whether the user asked to skip injection via $PLUG_NO_INJECT.
func Disabled() bool {
	v := os.Getenv(EnvVarDisable)
	return v != "" && v != "0"
}

// Available reports whether an embedded hook library exists for this platform.
func Available() bool {
	name := assetName()
	if name == "" {
		return false
	}
	f, err := libassets.Open(name)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// libDir is where plug writes the extracted hook library (~/.plug/lib).
func libDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".plug", "lib"), nil
}

// extractLib writes the embedded library for this platform to ~/.plug/lib and
// returns its path. The file name carries a content hash so a plug upgrade that
// changes the hook lands at a new path (no stale-lib reuse, and concurrent runs
// on different versions don't clash). Extraction is idempotent.
func extractLib() (string, error) {
	name := assetName()
	if name == "" {
		return "", fmt.Errorf("inject: no hook library for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	data, err := fs.ReadFile(libassets, name)
	if err != nil {
		return "", fmt.Errorf("inject: reading embedded hook: %w", err)
	}

	dir, err := libDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)
	ext := filepath.Ext(name) // .dylib / .so
	dst := filepath.Join(dir, fmt.Sprintf("plug-hook-%s%s", hex.EncodeToString(sum[:8]), ext))

	// Already present with the right size? Reuse it.
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(data)) {
		return dst, nil
	}
	// Write atomically: temp file in the same dir, then rename.
	tmp, err := os.CreateTemp(dir, ".plug-hook-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Prepare extracts the hook library for this platform and returns its path. It
// returns ok=false — with a one-line note via logf (may be nil) — when injection
// is disabled, unavailable on this platform, or extraction fails, so the caller
// falls back to the env-proxy cleanly.
func Prepare(logf func(string, ...any)) (string, bool) {
	note := func(format string, a ...any) {
		if logf != nil {
			logf(format, a...)
		}
	}
	if Disabled() {
		note("injection disabled (%s set)", EnvVarDisable)
		return "", false
	}
	if !Available() {
		note("injection unavailable for %s/%s — env-proxy only", runtime.GOOS, runtime.GOARCH)
		return "", false
	}
	lib, err := extractLib()
	if err != nil {
		note("injection off: %v", err)
		return "", false
	}
	return lib, true
}

// Env returns the extra environment entries that load the hook into the child
// and point it at the SOCKS proxy at socksAddr ("host:port"). It returns nil
// (injection skipped) when disabled, unavailable on this platform, or if
// extraction fails — in all those cases plug still works through the env-proxy.
//
// logf (may be nil) receives a one-line note about what was decided.
func Env(socksAddr string, logf func(string, ...any)) []string {
	lib, ok := Prepare(logf)
	if !ok {
		return nil
	}
	env := []string{
		PreloadVar() + "=" + AppendPreload(os.Getenv(PreloadVar()), lib),
		EnvVarSocks + "=" + socksAddr,
	}
	if logf != nil {
		logf("injection on — %s (transparent connect/DNS via SOCKS)", filepath.Base(lib))
	}
	return env
}

// AppendPreload adds lib to an existing loader-list value. macOS and Linux both
// separate multiple entries; macOS uses ':' for DYLD_INSERT_LIBRARIES, as does
// Linux for LD_PRELOAD (space also works on Linux, but ':' is safe on both).
func AppendPreload(existing, lib string) string {
	if existing == "" {
		return lib
	}
	return existing + ":" + lib
}
