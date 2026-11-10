package main

import (
	"path/filepath"
	"strings"
	"sync"
)

// The command's ARGUMENTS, repointed by the rule the variables already follow
// (localizeFileEnv, localizeVolumeEnv). Inside a session, a cluster path of the
// workload means the workload's file: a command written with the pod's paths
// runs unchanged here.
//
// The case that asked for it is a dev server taking over an HTTPS UI:
//
//	plug -s ui:443:PLUG_PORT polyglot --port={PLUG_PORT} --ssl-cert=/etc/tls/tls.crt
//
// The pod mounts its certificate Secret at /etc/tls, plug brings the files over
// and repoints the variables that name them, and the argument still said
// /etc/tls/tls.crt: ENOENT. The only way round was a variable declared in the
// pod for no other reason and a `sh -c '… "$TLS_CERT_FILE"'` around the command.
//
// Three shapes, and nothing else:
//
//   - the whole argument, which is also the separate value of `--opt /x`;
//   - the value of `--opt=/x` and `-Dkey=/x`, the part after the FIRST `=` of
//     an argument that starts with a dash.
//
// Never a substring of a longer string, and never a shell line: `sh -c '…'`
// is one argument, its paths are the shell's to expand, and that case keeps
// using the variables. Guessing inside free text is how a path in a URL, a
// regexp or a message would get rewritten without anyone asking.
//
// --no-repoint leaves every argument as written, for the rare command that
// names a LOCAL path equal to a cluster path. The variables are repointed
// either way: a variable is the workload's, an argument is yours.

// projectedFiles is what each workload's projection brought onto this machine,
// recorded by projectWorkloadEnv for the arguments to be repointed once every
// projection has run. Volumes are already recorded per workload (autoMounts).
var projectedFiles = struct {
	sync.Mutex
	by map[string]broughtFiles
}{by: map[string]broughtFiles{}}

// broughtFiles is one workload's fetched files: the session temp they landed
// under, and the cluster paths they came from.
type broughtFiles struct {
	dir   string
	paths []string
}

func recordProjectedFiles(name, dir string, paths []string) {
	projectedFiles.Lock()
	defer projectedFiles.Unlock()
	projectedFiles.by[name] = broughtFiles{dir: dir, paths: paths}
}

func projectedFilesOf(name string) broughtFiles {
	projectedFiles.Lock()
	defer projectedFiles.Unlock()
	return projectedFiles.by[name]
}

// argSource is one workload an argument may name a path of: its fetched files
// and its live volumes, in the order a path is looked up in them.
type argSource struct {
	name   string
	dir    string
	paths  []string
	mounts []volumeMount
}

// sessionArgSources lists the session's workloads in the order a path is
// looked up: the -s ones as they were written, then --env-of. Only the -s one
// a projection really ran for has anything recorded, so a -s that created a
// fresh name (nothing to park) simply contributes nothing.
func sessionArgSources(cfg config) []argSource {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, s := range cfg.exposes {
		add(s.Name)
	}
	add(cfg.envPolicy.from)
	var out []argSource
	for _, n := range names {
		f := projectedFilesOf(n)
		m := autoMountsFor(n)
		if f.dir == "" && len(m) == 0 {
			continue
		}
		out = append(out, argSource{name: n, dir: f.dir, paths: f.paths, mounts: m})
	}
	return out
}

// argHit is what happened to one argument that names a workload's path.
type argHit struct {
	before, after string
	// workload is the one whose copy the argument now names, or, when missing,
	// the first that mounts the path.
	workload string
	// missing: the path is under a mount, but no workload brought that file
	// over. The argument keeps the pod's path, and the session says so.
	missing bool
	// mountedBy lists every workload that mounts the path, when more than one
	// does: the first one's copy is used, and the session says which.
	mountedBy []string
}

// repointArgs rewrites the arguments that name a path of the sources, and
// reports each one it looked at. Pure (fileExists aside), so the rule is
// proven rather than reasoned about.
func repointArgs(args []string, srcs []argSource) ([]string, []argHit) {
	if len(srcs) == 0 {
		return args, nil
	}
	out := make([]string, len(args))
	var hits []argHit
	for i, arg := range args {
		out[i] = arg
		prefix, v := splitArgPath(arg)
		if v == "" {
			continue
		}
		var users []string
		local, from := "", ""
		for _, s := range srcs {
			l, mounted := s.localPath(v)
			if !mounted {
				continue
			}
			users = append(users, s.name)
			if local == "" && l != "" {
				local, from = l, s.name
			}
		}
		if len(users) == 0 {
			continue
		}
		h := argHit{before: arg, after: arg, workload: from}
		if len(users) > 1 {
			h.mountedBy = users
		}
		if local == "" {
			h.missing, h.workload = true, users[0]
		} else {
			h.after = prefix + local
			out[i] = h.after
		}
		hits = append(hits, h)
	}
	return out, hits
}

// splitArgPath finds the path an argument may carry: the whole of it, or what
// follows the first `=` of a dashed option. Empty when there is none, so a
// relative path, a URL or a plain word is never looked at.
func splitArgPath(arg string) (prefix, path string) {
	if strings.HasPrefix(arg, "/") {
		return "", arg
	}
	if strings.HasPrefix(arg, "-") {
		if eq := strings.IndexByte(arg, '='); eq > 0 && strings.HasPrefix(arg[eq+1:], "/") {
			return arg[:eq+1], arg[eq+1:]
		}
	}
	return "", ""
}

// localPath is where v lives on this machine for this workload, and whether
// the workload mounts it at all. The longest cluster path wins between its
// files and its volumes, as it does between volumes: a Secret mounted at
// /data/certs inside a volume at /data is the Secret's.
//
// A file is only named once it is THERE, as for a variable: one the agent could
// not bring over keeps the pod's path. A volume is live, so a path under it is
// named whether or not it exists yet: a log the process is about to create is
// still the volume's.
func (s argSource) localPath(v string) (local string, mounted bool) {
	best := ""
	isFile := false
	if s.dir != "" {
		for _, p := range s.paths {
			if (v == p || strings.HasPrefix(v, p+"/")) && len(p) > len(best) {
				best, isFile = p, true
			}
		}
	}
	var vol volumeMount
	for _, m := range s.mounts {
		if (v == m.cluster || strings.HasPrefix(v, m.cluster+"/")) && len(m.cluster) > len(best) {
			best, isFile, vol = m.cluster, false, m
		}
	}
	switch {
	case best == "":
		return "", false
	case isFile:
		if l := filepath.Join(s.dir, v); fileExists(l) {
			return l, true
		}
		return "", true
	default:
		return filepath.Join(vol.local, filepath.FromSlash(strings.TrimPrefix(v, vol.cluster))), true
	}
}

// repointSessionArgs is the session's half: run once every projection has
// landed and before the command starts, on each path that starts one. Says
// how many arguments each workload's paths took, which ones keep the pod's
// path and why, and which workload's copy was used when two mount the path.
func repointSessionArgs(cfg config, args []string) []string {
	if cfg.sidecar {
		// --dockerrun: the container gets the files at their cluster path,
		// and the sidecar's own command names none.
		return args
	}
	out, hits := repointArgs(args, sessionArgSources(cfg))
	if len(hits) == 0 {
		return args
	}
	if cfg.noRepoint {
		n := 0
		for _, h := range hits {
			if !h.missing {
				n++
			}
		}
		if n > 0 {
			info("--no-repoint: %d argument(s) naming a workload's mounted path left as written", n)
		}
		return args
	}
	var order []string
	count := map[string]int{}
	for _, h := range hits {
		if len(h.mountedBy) > 1 {
			info("%s is mounted by %s; %s's is used", h.before, strings.Join(h.mountedBy, " and "), h.workload)
		}
		if h.missing {
			info("%s: argument %s keeps the pod's path: the file it names was not brought over", h.workload, h.before)
			continue
		}
		if count[h.workload] == 0 {
			order = append(order, h.workload)
		}
		count[h.workload]++
	}
	for _, w := range order {
		info("%s: %d argument(s) repointed", w, count[w])
	}
	return out
}
