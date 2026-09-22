package main

import (
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
// (a bare --no-env), or a list of keys to leave out.
type envPolicy struct {
	off  bool
	drop map[string]bool
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
	if p.off {
		return nil, nil
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
	}
	return set, kept
}

// applyWorkloadEnv sets the merged variables on this process.
func applyWorkloadEnv(set map[string]string) {
	for k, v := range set {
		_ = os.Setenv(k, v)
	}
}
