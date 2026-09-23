package main

import (
	"errors"
	"os"
	"strings"
)

// The environment of the workload a session replaces, handed to the process
// that replaces it. The agent answers `env-of <name>` with the workload's
// variables; this side decides what the command finally sees.
//
// Three layers, in this order, and the order is the whole rule:
//
//  1. the workload's variables, as the agent read them;
//  2. minus what --no-env names (or all of them, with a bare --no-env);
//  3. the caller's own environment on top, untouched: a variable the person
//     set in the shell or in cross-env WINS over the cluster's. Otherwise
//     there would be no way to point a plugged service at a test database.
//
// Pure, so the rule is proven rather than reasoned about, and applied with
// os.Setenv on this process before the command starts: the child inherits
// os.Environ() as it always did, PATH handling and privilege drop included.

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

// mergeWorkloadEnv returns the variables to SET on this process: the workload's
// lines that survive the policy and that the caller's environment does not
// already define. Keys the caller has are returned in `kept` so the session can
// say which of the cluster's values it left alone.
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
		if !ok || k == "" || p.drop[k] {
			continue
		}
		if have[k] {
			kept = append(kept, k)
			continue
		}
		set[k] = v
		if v == "" {
			empty = append(empty, k)
		}
	}
	return set, kept, empty
}

// applyWorkloadEnv sets the merged variables on this process.
func applyWorkloadEnv(set map[string]string) {
	for k, v := range set {
		_ = os.Setenv(k, v)
	}
}
