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
	if strings.HasPrefix(key, "KUBERNETES_") || strings.HasPrefix(key, "PLUG_") {
		return true
	}
	// The service links kube injects for every Service in the namespace:
	// FOO_SERVICE_HOST, FOO_SERVICE_PORT, FOO_PORT=tcp://ip:port, and the
	// FOO_PORT_<n>_TCP{,_ADDR,_PORT,_PROTO} family. Read off a real pod: sixty
	// of them for a namespace of twenty services, every one a ClusterIP that
	// exists nowhere but inside the cluster. An application's own FOO_PORT=3000
	// is not a tcp:// URL, and that is the difference: only the kube shapes go.
	return strings.HasSuffix(key, "_SERVICE_HOST") || strings.HasSuffix(key, "_SERVICE_PORT") ||
		strings.Contains(key, "_SERVICE_PORT_") || kubeServiceLinkPort(key)
}

// kubeServiceLinkPort matches FOO_PORT_<n>_TCP, FOO_PORT_<n>_TCP_ADDR,
// FOO_PORT_<n>_TCP_PORT, FOO_PORT_<n>_TCP_PROTO and their UDP twins: the docker
// links kube still emulates. A bare FOO_PORT is decided by its VALUE (see
// envLines): "tcp://…" is a link, "3000" is the application's.
func kubeServiceLinkPort(key string) bool {
	i := strings.Index(key, "_PORT_")
	if i < 0 {
		return false
	}
	rest := key[i+len("_PORT_"):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return false
	}
	rest = rest[j:]
	return rest == "_TCP" || rest == "_UDP" || strings.HasPrefix(rest, "_TCP_") || strings.HasPrefix(rest, "_UDP_")
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
		if strings.HasSuffix(k, "_PORT") && (strings.HasPrefix(v, "tcp://") || strings.HasPrefix(v, "udp://")) {
			continue // a kube service link, not the application's own port
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
