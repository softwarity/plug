// plug-sign signs the CLI binaries a release publishes, so the launcher can
// refuse to execute anything else with privilege.
//
// It runs inside the image build, where the binaries are produced, with the
// private key arriving through a BuildKit secret mount: the key is readable for
// the length of one RUN and lands in no layer. When no key is mounted, which is
// every local `docker build` and every fork, it signs nothing and says so. The
// launcher tolerates an unsigned core until the cutover date compiled into it,
// so an unsigned image is a warning rather than a broken build.
//
// The statement it signs is the one cli/release_sig.go verifies. The two must
// agree exactly, which is why the format lives in a comment in both places:
//
//	plug-core-v1\n<os>-<arch>\n<version>\n<sha256 hex>\n
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: plug-sign <key file> <bin dir> <version>")
		os.Exit(2)
	}
	keyFile, binDir, version := os.Args[1], os.Args[2], strings.TrimSpace(os.Args[3])

	raw, err := os.ReadFile(keyFile)
	if err != nil {
		fail("cannot read the release key: %v", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		fail("the release key is not base64: %v", err)
	}
	if len(seed) != ed25519.PrivateKeySize {
		fail("the release key is %d bytes, expected %d", len(seed), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(seed)

	entries, err := os.ReadDir(binDir)
	if err != nil {
		fail("cannot list %s: %v", binDir, err)
	}
	signed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "plug-") || strings.HasSuffix(name, ".sig") {
			continue
		}
		osArch := strings.TrimSuffix(strings.TrimPrefix(name, "plug-"), ".exe")
		path := filepath.Join(binDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			fail("cannot read %s: %v", path, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(data))
		stmt := "plug-core-v1\n" + osArch + "\n" + strings.TrimPrefix(version, "v") + "\n" + sum + "\n"
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(stmt)))
		if err := os.WriteFile(path+".sig", []byte(sig+"\n"), 0o644); err != nil {
			fail("cannot write the signature for %s: %v", name, err)
		}
		fmt.Printf("plug-sign: %s %s\n", name, sum[:12])
		signed++
	}
	// A release that silently signed nothing would publish an image the launcher
	// refuses after the cutover, and the build is the only place that can still
	// tell the difference.
	if signed == 0 {
		fail("no plug-<os>-<arch> binary found in %s: the release would ship unsigned", binDir)
	}
	fmt.Printf("plug-sign: signed %d binaries for %s\n", signed, version)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "plug-sign: "+format+"\n", a...)
	os.Exit(1)
}
