package agent

import (
	"sort"
	"strings"
)

// The environment of the workload a session replaces, handed to the process
// that replaces it. A -s that takes a deployed service's place gives that
// process the service's variables by default: "your process behaves as it
// would inside the cluster" is the whole promise, and the configuration is
// part of it. Until now the credentials a developer needed ended up in
// package.json, in the clear, retyped from the deployment.
//
// Read from the RUNNING process where the platform allows it (docker inspect
// hands Config.Env already resolved; on Kubernetes /proc/1/environ inside the
// parked pod), so what came from a Secret arrives as the pod already has it,
// with no extra right to read Secrets. The verb answers "KEY=VALUE" lines,
// one per variable, values as they are; the client does the merging.

// envExcluded is what a process must NOT inherit from a container: what
// describes the container's own runtime rather than the application, and
// what the client machine sets for itself. mirrord keeps the same kind of
// list. KUBERNETES_* and the *_SERVICE_HOST/PORT pairs kube injects are the
// ones that bite hardest: an address that only exists inside the cluster,
// handed to a process outside it.
func envExcluded(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "TMPDIR", "TMP", "TEMP",
		"HOSTNAME", "TERM", "LANG", "LC_ALL", "SHLVL", "_",
		"GOPATH", "GOROOT", "GOFLAGS", "PYTHONPATH", "PYTHONHOME", "JAVA_HOME", "JAVA_TOOL_OPTIONS",
		"NODE_PATH", "RUBYLIB", "GEM_HOME", "GEM_PATH", "BUNDLE_PATH", "LD_LIBRARY_PATH", "LD_PRELOAD",
		"DYLD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES":
		return true
	}
	return strings.HasPrefix(key, "KUBERNETES_") ||
		strings.HasSuffix(key, "_SERVICE_HOST") || strings.HasSuffix(key, "_SERVICE_PORT") ||
		strings.Contains(key, "_SERVICE_PORT_") || strings.HasPrefix(key, "PLUG_")
}

// envLines renders "KEY=VALUE" lines from raw environ entries, dropping the
// excluded keys, malformed entries and duplicates (last one wins, as execve
// does), sorted so the answer is stable for the tests and the eye.
func envLines(raw []string) []string {
	seen := map[string]string{}
	var order []string
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || envExcluded(k) {
			continue
		}
		if _, dup := seen[k]; !dup {
			order = append(order, k)
		}
		seen[k] = v
	}
	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+seen[k])
	}
	return out
}

// procEnviron parses the NUL-separated content of /proc/<pid>/environ.
func procEnviron(b []byte) []string {
	var out []string
	for _, kv := range strings.Split(string(b), "\x00") {
		if kv != "" {
			out = append(out, kv)
		}
	}
	return out
}
