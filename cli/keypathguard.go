package main

import (
	"os"
	"strings"
)

// keyPathRefusal is the decision behind the Windows key-path guard, kept here
// without a build tag ON PURPOSE.
//
// The rule it encodes only matters on Windows, where a machine-wide SYSTEM
// service reads a key path out of a directory plain users can write. But a rule
// that compiles only on Windows is a rule that runs on one CI leg and can be
// exercised on nobody's machine, and this one decides what a service running as
// the most privileged account on the box will open. So the decision is pure and
// testable everywhere, and only the filesystem lookup around it is per-OS.
//
// It returns why the path must be refused, or "" to allow it.
func keyPathRefusal(abs string, mode os.FileMode, systemRoots []string) string {
	if !mode.IsRegular() {
		return "it is not a regular file. plug can run as a machine-wide service here, and a\n" +
			"      pipe or a device in place of a key file is how that privilege gets borrowed"
	}
	// Normalised in WINDOWS terms, not with path/filepath. filepath follows the
	// HOST separator, so on a developer's mac or on a linux CI leg it would leave
	// every backslash in place, compare nothing, and this rule would quietly allow
	// everything while its tests passed. The rule is about Windows paths; it has
	// to be written in them wherever it runs.
	norm := func(p string) string {
		p = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(p, "/", `\`)))
		for strings.Contains(p, `\\`) {
			p = strings.ReplaceAll(p, `\\`, `\`)
		}
		return strings.TrimSuffix(p, `\`)
	}
	lower := norm(abs)
	for _, root := range systemRoots {
		root = norm(root)
		if root == "" {
			continue
		}
		// On a SEPARATOR boundary, never on the raw string: "C:\WindowsApps" starts
		// with "C:\Windows" and is not inside it, and a rule that refused it would
		// be refusing the wrong thing while looking correct.
		if lower == root || strings.HasPrefix(lower, root+`\`) {
			return "it is under a system directory. Keys live in your own profile; plug can run as\n" +
				"      a machine-wide service here, so a key path pointing into the system tree is a\n" +
				"      way to have it open a file the caller could not open themselves"
		}
	}
	return ""
}

// systemRootsForKeyGuard lists the trees a key must never be read from. Built
// from the environment first so a machine whose Windows lives off C: is covered,
// with the usual paths kept as a floor: the environment is read from a process
// the caller may have influenced, so it is a source of MORE roots, never of fewer.
func systemRootsForKeyGuard() []string {
	return []string{
		os.Getenv("SystemRoot"),
		os.Getenv("ProgramData"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		`C:\Windows`,
		`C:\Program Files`,
		`C:\Program Files (x86)`,
		`C:\ProgramData`,
	}
}
