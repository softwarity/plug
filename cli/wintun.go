package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// wintun.dll, the driver the Windows datapath loads, comes from the agent like
// the binaries do, and it is loaded by the SYSTEM service that runs plug.exe.
// Until this file existed the only check on it was its size: anything over a
// hundred kilobytes that an agent chose to serve was written next to the
// service's binary. That is the same hole the core and the launcher had before
// release signatures, in the most privileged process plug has on Windows.
//
// So the DLL is attested the way the core is. The agent answers `digest wintun`
// (and `digest wintun-arm64`) with the sha256 and the release signature written
// at image build time by cli/cmd/plug-sign, and the bytes go through
// admitPrivilegedBytes before any WriteFile. An agent that cannot attest leaves
// whatever DLL is already installed exactly as it is: unverified bytes are never
// written, and never replace a driver that works.
//
// No build tag, deliberately: the decision is exercised on every CI leg and on
// every developer's machine, while only the two callers are Windows-only.

// wintunVerb is the download verb for this machine's driver. `wintun` is the
// amd64 driver and stays that way: every Windows launcher already installed
// asks for it by that bare name, and making it architecture-dependent would
// hand an arm64 driver to an amd64 machine on its next update.
func wintunVerb() string {
	if runtime.GOARCH == "arm64" {
		return "wintun-arm64"
	}
	return "wintun"
}

// wintunLabel is what the release signature over the DLL names in place of an
// os-arch: the signer derives it from the file it signed (wintun-<arch>.dll),
// and it is distinct from every core label on purpose, so a signature issued
// for a driver can never be lifted onto a binary, nor the reverse.
func wintunLabel() string { return "wintun-" + runtime.GOARCH }

// admitWintun decides whether these driver bytes may be written where the
// SYSTEM service loads them: the agent's attestation for the driver, and the
// same three refusals the core gets.
func admitWintun(cfg config, dll []byte) error {
	if len(dll) < 100_000 || !bytes.HasPrefix(dll, []byte("MZ")) {
		return fmt.Errorf("wintun.dll looks invalid (%d bytes)", len(dll))
	}
	att, derr := fetchDigest(cfg, wintunVerb())
	got := fmt.Sprintf("%x", sha256.Sum256(dll))
	return admitPrivilegedBytes(att, derr, wintunLabel(), got, "wintun.dll")
}

// refreshWintun fetches the driver from the agent and, once admitted, installs
// it in dir, which is the launcher's own directory on Windows: the one the
// SYSTEM service runs plug.exe from. Any refusal leaves the existing file
// untouched.
func refreshWintun(cfg config, dir string) error {
	dll, err := getDownload(cfg, wintunVerb(), "wintun.dll")
	if err != nil {
		return err
	}
	if err := admitWintun(cfg, dll); err != nil {
		return err
	}
	return writeWintun(dir, dll)
}

// writeWintun puts admitted bytes at dir/wintun.dll through a temp file and a
// rename, so a loader never sees a half-written driver.
func writeWintun(dir string, dll []byte) error {
	tmp, err := os.CreateTemp(dir, ".wintun-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(dll); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "wintun.dll"))
}
