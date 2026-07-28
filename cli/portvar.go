package main

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// A -s mapping's third field is the LOCAL port the cluster name forwards to.
// Pinning it means every project on the machine has to agree on who owns which
// number, and two branches of the same app can't run side by side. Naming it
// instead lets plug pick a free one per session:
//
//	plug -s web:8080:PORT  npm run dev -- --port={PORT}
//
// PORT declares the variable (bare — the third field of a -s can only ever be a
// port, so there is nothing to disambiguate), {PORT} references it in the
// child's argv (braced — argv is free text, and a bare PORT would also match
// --transport=PORTAL). The same name is exported to the child, so a command that
// already reads an env var needs no flag at all:
//
//	plug -s web:8080:PROXY_PORT  polyglot --config=./angular.json
//
// Declaring without referencing is fine (that second form). The reverse is not:
// a {TOKEN} nobody declared is a typo, and typos here are silent — the child
// would just receive a literal "{PROT}" and bind something else.

// portVarName is the token accepted in a -s third field and inside {…}. It is
// exported as an environment variable, so it must be a valid one: a letter or
// underscore, then letters, digits and underscores.
var portVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// portVarRef matches a {TOKEN} reference in the child's argv.
var portVarRef = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}`)

// freeLocalPort asks the OS for a port nobody is listening on. It is released
// immediately and handed to the child, which binds it a moment later — a window
// another process could theoretically win. In practice the ephemeral range is
// wide and the kernel does not hand the same port out twice in a row, and the
// alternative (holding the socket) can't work: the child needs to bind it
// itself. A loser fails loudly on EADDRINUSE, never silently.
func freeLocalPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", err
	}
	return port, nil
}

// hasPortVars reports whether any spec named its local port rather than pinning
// it. Nothing below runs otherwise: a session that doesn't use the feature must
// not have its argv rewritten, nor its `awk '{print}'` rejected as a bad
// reference.
func hasPortVars(specs []tunnel.ExposeSpec) bool {
	for _, s := range specs {
		if s.PortVar != "" {
			return true
		}
	}
	return false
}

// resolvePortVars allocates a local port for every named -s, exports it to the
// child as $NAME, and substitutes {NAME} throughout the child's argv. Returns
// the specs with LocalPort filled in and the rewritten argv.
//
// Runs in the process that will spawn the child — the core — so the env is set
// on ourselves and inherited (every platform's child path starts from
// os.Environ(); none overrides Env with a curated list).
func resolvePortVars(specs []tunnel.ExposeSpec, cmdArgs []string) ([]tunnel.ExposeSpec, []string, error) {
	if !hasPortVars(specs) {
		return specs, cmdArgs, nil
	}
	ports := make(map[string]string, len(specs)) // NAME -> allocated port
	out := make([]tunnel.ExposeSpec, len(specs))
	copy(out, specs)
	for i, s := range out {
		if s.PortVar == "" {
			continue
		}
		// The same name twice means one port for both mappings — declaring
		// -s a:80:PORT -s b:81:PORT points two cluster names at one process.
		port, seen := ports[s.PortVar]
		if !seen {
			p, err := freeLocalPort()
			if err != nil {
				return nil, nil, fmt.Errorf("-s %s:%s:%s: cannot allocate a local port: %w",
					s.Name, s.ClusterPort, s.PortVar, err)
			}
			port = p
			ports[s.PortVar] = port
			if err := os.Setenv(s.PortVar, port); err != nil {
				return nil, nil, fmt.Errorf("-s %s: cannot export %s: %w", s.Name, s.PortVar, err)
			}
		}
		out[i].LocalPort = port
	}
	args, err := substitutePortVars(cmdArgs, ports)
	if err != nil {
		return nil, nil, err
	}
	return out, args, nil
}

// substitutePortVars replaces every {NAME} in argv with its port. An unknown
// {TOKEN} is an error, not a literal: it is either a typo in a name that WAS
// declared, or a reference to a -s that isn't there — both silent bugs if left
// to reach the child.
func substitutePortVars(cmdArgs []string, ports map[string]string) ([]string, error) {
	out := make([]string, len(cmdArgs))
	for i, arg := range cmdArgs {
		var bad string
		out[i] = portVarRef.ReplaceAllStringFunc(arg, func(ref string) string {
			name := ref[1 : len(ref)-1]
			if port, ok := ports[name]; ok {
				return port
			}
			if bad == "" {
				bad = name
			}
			return ref
		})
		if bad != "" {
			return nil, fmt.Errorf("{%s} in %q references no -s mapping — declared: %s\n"+
				"declare it by naming the local port: -s <name>:<cluster-port>:%s",
				bad, arg, declaredList(ports), bad)
		}
	}
	return out, nil
}

// declaredList renders the declared names for an error message, sorted so the
// text is stable run to run.
func declaredList(ports map[string]string) string {
	if len(ports) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, "{"+name+"}")
	}
	sortStrings(names)
	return strings.Join(names, ", ")
}

// sortStrings is insertion sort — the slice is the number of -s flags on one
// command line, and this saves pulling "sort" into main's import set.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
