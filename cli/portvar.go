package main

import (
	"fmt"
	"net"
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
// PORT declares (bare — the third field of a -s can only ever be a port, so
// there is nothing to disambiguate), {PORT} references it in the child's argv
// (braced — argv is free text, and a bare PORT would also match
// --transport=PORTAL).
//
// The command line is the ONLY channel: nothing is exported to the child's
// environment. Two ways to hand over one number would be two things to keep in
// agreement, and injecting a variable of the user's choosing means quietly
// overwriting whatever the application already kept under that name.
//
// So the two halves are required to match, both ways. A {TOKEN} nobody declared
// is a typo — left alone the child receives the literal "{PROT}" and binds
// something else. A declaration nobody references is the same bug seen from the
// other end: plug would allocate a port the process never hears about, bind the
// cluster name to it, and nothing would ever answer.

// portVarName is the token accepted in a -s third field and inside {…}: a letter
// or underscore, then letters, digits and underscores. Deliberately the shape of
// an identifier — it reads as a name rather than as a mangled port, and it can't
// collide with the digits it stands in for.
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

// resolvePortVars allocates a local port for every named -s and substitutes
// {NAME} throughout the child's argv. Returns the specs with LocalPort filled in
// and the rewritten argv.
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
		}
		out[i].LocalPort = port
	}
	args, used, err := substitutePortVars(cmdArgs, ports)
	if err != nil {
		return nil, nil, err
	}
	// A name that reached no reference is a port the child will never hear
	// about — the mapping would be armed and simply never answer. Say so here,
	// where the fix is obvious, rather than leave it to be debugged from the
	// far end of the cluster.
	for _, s := range out {
		if s.PortVar != "" && !used[s.PortVar] {
			return nil, nil, fmt.Errorf("-s %s:%s:%s names the local port, but {%s} appears nowhere "+
				"in the command — plug would pick a port your process is never told about.\n"+
				"Pass it: … --port={%s}   (or pin the number: -s %s:%s:<port>)",
				s.Name, s.ClusterPort, s.PortVar, s.PortVar, s.PortVar, s.Name, s.ClusterPort)
		}
	}
	return out, args, nil
}

// substitutePortVars replaces every {NAME} in argv with its port, and reports
// which names were actually referenced. An unknown {TOKEN} is an error, not a
// literal: it is either a typo in a name that WAS declared, or a reference to a
// -s that isn't there — both silent bugs if left to reach the child.
func substitutePortVars(cmdArgs []string, ports map[string]string) ([]string, map[string]bool, error) {
	out := make([]string, len(cmdArgs))
	used := make(map[string]bool, len(ports))
	for i, arg := range cmdArgs {
		var bad string
		out[i] = portVarRef.ReplaceAllStringFunc(arg, func(ref string) string {
			name := ref[1 : len(ref)-1]
			if port, ok := ports[name]; ok {
				used[name] = true
				return port
			}
			if bad == "" {
				bad = name
			}
			return ref
		})
		if bad != "" {
			return nil, nil, fmt.Errorf("{%s} in %q references no -s mapping — declared: %s\n"+
				"declare it by naming the local port: -s <name>:<cluster-port>:%s",
				bad, arg, declaredList(ports), bad)
		}
	}
	return out, used, nil
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
