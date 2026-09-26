package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The environment of the workload a session replaces, handed to the process
// that replaces it. The agent answers `env-of <name>` with the workload's
// variables; this side decides what the command finally sees.
//
// The rule, and the order is the whole of it:
//
//  1. the workload's variables, as the agent read them, WIN. The cluster is the
//     source of truth: a process joining the cluster should behave as it does
//     inside it, not drift with whatever the shell, a .env or a CI runner left
//     in the environment. So a projected value OVERWRITES an inherited one.
//  2. except what --no-env names: a bare --no-env projects nothing, and
//     --no-env A,B keeps YOUR value for A and B (the explicit escape when you
//     do want to point a key at, say, a test database).
//
// This inverts the earlier rule where the caller won by default: that made the
// result depend on the launched process (a cross-env, a .env, a stray export),
// which is exactly what "behaves as in the cluster" must not do.
//
// One honest limit, structural: plug sets the environment BEFORE it execs the
// command, so a value the command sets ITSELF afterwards - `cross-env VAR=…` in
// the command line, a .env loaded with override - is beyond plug's reach. The
// cluster wins over everything INHERITED; a runtime assignment the process makes
// is its own.
//
// Pure, so the rule is proven rather than reasoned about, and applied with
// os.Setenv on this process before the command starts: the child inherits
// os.Environ(), PATH handling and privilege drop included.

// envPolicy is what --no-env said: nothing (project everything), everything
// (a bare --no-env), or a list of keys to leave out; and what --env-of said,
// which workload's environment to take when it is not the one -s parks.
type envPolicy struct {
	off  bool
	drop map[string]bool
	// from is the --env-of name: the workload whose environment the command
	// gets, whatever -s does. Empty means the parked one, the default. With it,
	// a -c gets an environment too (a one-off script run with a service's
	// credentials), and a -s can borrow another service's (the credentials of
	// the one it talks to rather than of the one it replaces).
	from string
}

// envPolicyOf builds the policy from the three flags as the launcher saw them,
// and refuses the one combination that says two things at once: a bare
// --no-env asks for no environment, --env-of asks for one.
func envPolicyOf(noEnv bool, noEnvList, envOf string) (envPolicy, error) {
	p := envPolicy{}
	if noEnv {
		p = parseNoEnv(noEnvList)
	}
	if envOf != "" {
		if p.off {
			return envPolicy{}, errors.New("--env-of and a bare --no-env contradict each other: one asks for a workload's environment, the other for none (--no-env A,B still leaves keys out of it)")
		}
		p.from = envOf
	}
	return p, nil
}

// parseNoEnv reads the value that followed --no-env. A bare flag has no value
// and turns projection off; "A,B" drops those keys.
func parseNoEnv(value string) envPolicy {
	if value == "" {
		return envPolicy{off: true}
	}
	p := envPolicy{drop: map[string]bool{}}
	for _, k := range strings.Split(value, ",") {
		if k = strings.TrimSpace(k); k != "" {
			p.drop[k] = true
		}
	}
	return p
}

// mergeWorkloadEnv returns the variables to SET on this process: every workload
// line the cluster wins on (all of them but the --no-env ones), overwriting an
// inherited value. `kept` names the keys --no-env held back to the caller's own
// value, so the session can say which the cluster did NOT touch.
func mergeWorkloadEnv(lines []string, callerEnv []string, p envPolicy) (set map[string]string, kept []string) {
	set, kept, _ = mergeWorkloadEnvWithEmpty(lines, callerEnv, p)
	return set, kept
}

// mergeWorkloadEnvWithEmpty is mergeWorkloadEnv that also names the keys whose
// value came through EMPTY: on Kubernetes without pods/exec the agent reads the
// pod spec, where a Secret or ConfigMap reference has no value, and a service
// that starts with an empty password fails in a way that names anything but
// the missing RBAC rule. The session says which keys those are, beside the
// count, so the cause is on the same line as the symptom.
func mergeWorkloadEnvWithEmpty(lines []string, callerEnv []string, p envPolicy) (set map[string]string, kept, empty []string) {
	if p.off {
		return nil, nil, nil
	}
	have := map[string]bool{}
	for _, kv := range callerEnv {
		if k, _, ok := strings.Cut(kv, "="); ok {
			have[k] = true
		}
	}
	set = map[string]string{}
	for _, kv := range lines {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if p.drop[k] {
			// --no-env=k: the cluster's value is held back; the caller's own
			// value (if it has one) stands, and is named in kept so the session
			// can say so.
			if have[k] {
				kept = append(kept, k)
			}
			continue
		}
		set[k] = v // the cluster wins, overwriting whatever was inherited
		if v == "" {
			empty = append(empty, k)
		}
	}
	return set, kept, empty
}

// envExecer is the one method readWorkloadEnv needs of a transport, so a test
// can stand in a fake and prove the negotiation on bytes.
type envExecer interface {
	ExecAll(cmd string) (string, error)
}

// workloadEnvReply is what the agent answered for one env-of: the KEY=VALUE
// lines and the "# " notes, already split, or agentErr when the agent could
// answer but refused ("error: …", e.g. a right it lacks).
type workloadEnvReply struct {
	vars     []string
	notes    []string
	agentErr string
}

// readWorkloadEnv asks the agent for name's environment, PREFERRING env-ofz,
// whose NUL-delimited reply carries a multi-line value (a PEM, a config blob)
// whole. An agent before that verb answers "unknown command"; the newline form
// (env-of) is the fallback, and it truncates any value that contains a newline
// at the first one - all an old agent could ever give. The records are the same
// either way: a "# " prefix is a note, everything else a KEY=VALUE line whose
// value may now itself contain newlines.
func readWorkloadEnv(tr envExecer, name string) (workloadEnvReply, error) {
	out, err := tr.ExecAll("env-ofz " + name)
	sep := "\x00"
	if err == nil && strings.HasPrefix(out, "error:") && strings.Contains(out, "unknown command") {
		out, err = tr.ExecAll("env-of " + name)
		sep = "\n"
	}
	if err != nil {
		return workloadEnvReply{}, err
	}
	if strings.HasPrefix(out, "error:") {
		return workloadEnvReply{agentErr: strings.TrimSpace(strings.TrimPrefix(out, "error:"))}, nil
	}
	var r workloadEnvReply
	for _, rec := range strings.Split(out, sep) {
		rec = strings.TrimRight(rec, "\r") // a legacy CRLF line; a NUL record has none
		switch {
		case rec == "":
		case strings.HasPrefix(rec, "# "):
			r.notes = append(r.notes, strings.TrimPrefix(rec, "# "))
		default:
			r.vars = append(r.vars, rec)
		}
	}
	return r, nil
}

// localizeFileEnv repoints the variables whose value names a projected mount
// path (a CA at /certificates/ca.crt, a keystore) at the local copy fetched
// under dir - this is option B: the files land in a session temp rather than at
// their absolute cluster path, so NODE_EXTRA_CA_CERTS, sslrootcert, SSL_CERT_FILE
// and their kind resolve without writing to the host's real /certificates. A
// value the app reads by a hard-coded path rather than through a variable is not
// redirected, and cannot be by this mechanism.
func localizeFileEnv(vars, mountPaths []string, dir string) []string {
	if dir == "" || len(mountPaths) == 0 {
		return vars
	}
	out := make([]string, len(vars))
	for i, kv := range vars {
		if k, v, ok := strings.Cut(kv, "="); ok && underAnyPath(v, mountPaths) {
			out[i] = k + "=" + filepath.Join(dir, v)
		} else {
			out[i] = kv
		}
	}
	return out
}

func underAnyPath(v string, paths []string) bool {
	for _, p := range paths {
		if v == p || strings.HasPrefix(v, p+"/") {
			return true
		}
	}
	return false
}

// applyWorkloadEnv sets the merged variables on this process.
func applyWorkloadEnv(set map[string]string) {
	for k, v := range set {
		_ = os.Setenv(k, v)
	}
}
