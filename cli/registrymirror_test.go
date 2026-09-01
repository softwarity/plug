package main

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// cli/registry.go says in its own header that its pure helpers are MIRRORED from
// agent/registry.go and must be kept in sync. Nothing checked it, and they
// drifted: four behaviours in registryToken, all of them this copy being the
// weaker one, so a registry that answered the cluster could fail here. The header
// was a promise nobody could keep.
//
// The repository already had this dispositif for another mirrored function
// (internal/tunnel/relaycopies_test.go) and that copy did NOT drift, which is the
// whole argument for writing this one. It compares parsed BODIES rather than
// searching for substrings, so an inverted argument or a dropped branch is caught
// too, which the substring approach would miss.
func TestTheRegistryMirrorsStillAgree(t *testing.T) {
	mine := funcBodies(t, "registry.go")
	theirs := funcBodies(t, filepath.Join("..", "agent", "registry.go"))

	// Two kinds of difference, and only one of them is drift.
	//
	// DELIBERATE: a behaviour that differs on purpose, with the reason written in
	// the code. Listed by name and left alone.
	//
	// KNOWN COSMETIC: the same behaviour written differently, a loop form or a
	// variable name. Syncing those would mean rewriting registry code in two
	// modules for no behaviour change, which is risk without benefit. So they are
	// PINNED instead: the pair's fingerprint is recorded, and the moment either
	// side is edited the fingerprint moves and this test fails, asking whoever
	// edited it to confirm the two still agree and to re-pin. That catches real
	// drift without demanding identity, which is what actually happened here:
	// registryToken drifted on four behaviours while these stayed equivalent.
	deliberate := map[string]string{
		"registryTagsWithin": "different time budgets, documented in cli/registry.go",
		"registryTags":       "different time budgets, documented in cli/registry.go",
		"releaseNewerThan":   "opposite default on an unparsable version, unreachable from the CLI path",
	}
	pinned := map[string]string{
		// same walk, `for i := 0; i < 3` against `for i := range`
		"versionLess": "f3ec39e0f0dbb22c",
		// same parse, one returns a zero literal where the other returns the zero value
		"parseExactRelease": "b4db1d3d333aa32c",
		// same scan, the guard is inverted and the loop body reordered
		"parseNextLink": "aa7635862c15ddcd",
		// same split, one accumulates into a Builder and the other slices
		"splitChallenge": "218fd7b67ea41cfa",
		// same normalisation, written in a different order
		"parseImageRef": "96b106dbdbd311a2",
		// same request and retry, different names for the locals
		"registryGET": "b97036accf625f05",
		// same four behaviours after the sync, the CLI parses the challenge into a map
		"registryToken": "b023ce45224a2177",
	}

	shared, reported := 0, 0
	for name, body := range mine {
		other, ok := theirs[name]
		if !ok {
			continue
		}
		shared++
		if body == other {
			continue
		}
		if why, allowed := deliberate[name]; allowed {
			t.Logf("%s differs on purpose: %s", name, why)
			continue
		}
		if want, ok := pinned[name]; ok {
			got := fingerprint(body, other)
			if want == "" {
				t.Logf("PIN %s = %q", name, got)
				reported++
				continue
			}
			if got != want {
				t.Errorf("%s was written differently on the two sides and one of them has just changed.\n"+
					"They were equivalent when pinned; check they still are, then update the pin to %q.\n"+
					"cli:\n%s\nagent:\n%s", name, got, body, other)
			}
			continue
		}
		t.Errorf("%s has drifted between cli/registry.go and agent/registry.go.\n"+
			"The CLI copy is the one that has historically fallen behind, so check which of the two is\n"+
			"right before syncing. If the difference is deliberate, say why in BOTH files and list it here.\n"+
			"cli:\n%s\nagent:\n%s", name, body, other)
	}
	if reported > 0 {
		t.Fatalf("%d pins are empty; paste the PIN lines above into the pinned map", reported)
	}
	// Without this, renaming a file or breaking the parser would leave the test
	// comparing nothing and reporting success, which is the exact failure mode it
	// exists to prevent.
	if shared < 5 {
		t.Fatalf("only %d functions are shared between the two registries; the extraction is broken", shared)
	}
}

// fingerprint identifies a PAIR of bodies, so a change to either side moves it.
func fingerprint(a, b string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a+"\x00"+b)))[:16]
}

// funcBodies returns the canonical text of every top-level function body in a
// file, keyed by name. Printed from the AST, so comments and formatting are out
// of the comparison and only what the code DOES is compared.
func funcBodies(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		var b strings.Builder
		if err := printer.Fprint(&b, fset, fn.Body); err != nil {
			t.Fatalf("printing %s in %s: %v", fn.Name.Name, path, err)
		}
		out[fn.Name.Name] = b.String()
	}
	return out
}
