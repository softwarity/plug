package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// THIRD_PARTY_LICENSES.md says of itself that it is generated from the actual
// link graph of the plug binary, union of the three platforms. It was not: two
// modules were linked into every shipped binary and named nowhere, and both
// carry licences that require their copyright notice to travel with the binary
// form. plug redistributes those binaries, from the agent image and from the
// install script, so the omission was an unmet obligation rather than an
// untidiness.
//
// Nothing was going to catch that by reading. This asks the toolchain.
func TestEveryLinkedModuleIsInTheLicenceFile(t *testing.T) {
	doc, err := os.ReadFile("../THIRD_PARTY_LICENSES.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)

	seen := map[string]bool{}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./...")
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("cannot resolve the %s link graph (%v)", goos, err)
		}
		for _, mod := range strings.Fields(string(out)) {
			if mod == "" || strings.HasPrefix(mod, "github.com/softwarity/plug") {
				continue // our own code is not a third party
			}
			seen[mod] = true
		}
	}
	if len(seen) < 5 {
		t.Fatalf("the link graph came back with %d modules, the extraction is broken", len(seen))
	}
	for mod := range seen {
		if !strings.Contains(text, mod) {
			t.Errorf("%s is linked into the shipped binary and THIRD_PARTY_LICENSES.md does not name it", mod)
		}
	}
}
