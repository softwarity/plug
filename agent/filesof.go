package agent

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

// files-of hands a plugged process the SECRET and CONFIGMAP files a workload
// has MOUNTED, the companion of env-of for what does not live in a variable: a
// CA bundle, a keystore, a config file. The agent reads them where they are,
// with the same exec it uses for the environment (no `get secrets`), and streams
// them as a tar. The client materialises them locally and points the process at
// them. One-shot at session start, because a mounted secret does not change
// under a running pod; a LIVE mount (a PVC's data) is a separate, opt-in idea.
//
// The reply is JSON: the absolute mount paths (so the client can repoint the
// variables that name them) and the tar, base64'd so it rides the same text
// protocol as every other verb, the "error:" convention and the version
// negotiation intact. An agent that does not know the verb answers "unknown
// command" and the client simply materialises nothing.
type filesReply struct {
	Paths []string `json:"paths"`
	Tar   string   `json:"tar"` // base64 of `tar cf -` over Paths, empty when there is nothing
}

func filesReplyJSON(paths []string, tar []byte) string {
	if paths == nil {
		paths = []string{}
	}
	b, _ := json.Marshal(filesReply{Paths: paths, Tar: base64.StdEncoding.EncodeToString(tar)})
	return string(b)
}

// k8sVolume and k8sMount are the slivers of a pod spec projectableMounts reads:
// which volumes carry secret/configMap/projected content, and where each is
// mounted.
type k8sVolume struct {
	Name      string          `json:"name"`
	Secret    json.RawMessage `json:"secret"`
	ConfigMap json.RawMessage `json:"configMap"`
	Projected json.RawMessage `json:"projected"`
}

type k8sMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// projectableMounts returns the mount paths worth handing over: those backed by
// a secret, configMap or projected volume. It DROPS the ServiceAccount token
// mount (/var/run/secrets/kubernetes.io/...) the kubelet injects into every pod
// - the token and the API CA are the cluster's own credentials, of no use to a
// process outside it and a needless thing to copy onto a laptop - exactly as
// envExcluded drops KUBERNETES_* from the environment.
func projectableMounts(vols []k8sVolume, mounts []k8sMount) []string {
	backed := map[string]bool{}
	for _, v := range vols {
		if len(v.Secret) > 0 || len(v.ConfigMap) > 0 || len(v.Projected) > 0 {
			backed[v.Name] = true
		}
	}
	var out []string
	for _, m := range mounts {
		if backed[m.Name] && !isServiceAccountMount(m.MountPath) {
			out = append(out, m.MountPath)
		}
	}
	return out
}

func isServiceAccountMount(path string) bool {
	return path == "/var/run/secrets/kubernetes.io/serviceaccount" ||
		strings.HasPrefix(path, "/var/run/secrets/kubernetes.io/")
}

// doFilesOf answers the mounted secret/configMap files of the workload behind
// <name>. Kubernetes only for now (Docker/Swarm is a later slice); anything it
// cannot read answers an empty set, which the client materialises as nothing -
// the env-of note already told the operator why (no exec, distroless, no pod).
func doFilesOf(cmd []string) {
	if len(cmd) != 2 || !nameRe.MatchString(cmd[1]) {
		answer("error: usage: files-of <name>")
	}
	name := cmd[1]
	var paths []string
	var tarball []byte
	switch {
	case k8sAvailable():
		paths, tarball = k8sFilesOf(k8sNamespace(), name)
	case dockerAvailable():
		paths, tarball = dockerFilesOf(name)
	}
	answer("%s", filesReplyJSON(paths, tarball))
}

// secretsMount is where both Compose (its `secrets:`) and Swarm mount a secret,
// and the one place the Docker side projects from: a bounded, conventional
// location, never a whole data volume.
const secretsMount = "/run/secrets"

// dockerFilesOf reads the parked container's mounted secret files. The parked
// container is stopped, but /containers/{id}/archive reads a stopped
// filesystem, so its /run/secrets tars out all the same. The container is found
// by the parking receipt the signpost carries, as dockerEnvOf finds it.
func dockerFilesOf(name string) ([]string, []byte) {
	self, err := dockerSelf()
	if err != nil {
		return nil, nil
	}
	if self.service != "" && swarmManager() {
		return swarmFilesOf(name, self)
	}
	// The parked (stopped) container first, then the running one a --env-of
	// names but no one parked - the same candidates env-of reads, so a mounted
	// file is found whether the workload was taken over or only borrowed.
	for _, id := range dockerNameCandidates(name, self) {
		if raw, code, err := dockerArchive(id, secretsMount); err == nil && code == 200 {
			return []string{secretsMount}, rerootTar(raw, secretsMount)
		}
	}
	return nil, nil
}

// swarmFilesOf hands over both a Swarm service's CONFIGS and its SECRETS. A
// config is readable through the API (docker config inspect returns the data),
// so it is read here and now. A SECRET is only a file inside a RUNNING task, and
// the takeover scaled the service to zero, so it cannot be read here - it was
// read at PARK time (swarmStashSecrets, while a task still ran) and kept in a
// stash on disk, which this merges in. Between them a Swarm service's mounted
// files come through whichever way they were mounted.
func swarmFilesOf(name string, self selfInfo) ([]string, []byte) {
	own := swarmNameOwner(name, self)
	if own == nil {
		return nil, nil
	}
	var s struct {
		Spec struct {
			TaskTemplate struct {
				ContainerSpec struct {
					Configs []struct {
						ConfigID string `json:"ConfigID"`
						File     struct {
							Name string `json:"Name"`
						} `json:"File"`
					} `json:"Configs"`
				} `json:"ContainerSpec"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+own.id, nil, &s); err != nil || code != 200 {
		return nil, nil
	}
	files := map[string][]byte{}
	var paths []string
	for _, c := range s.Spec.TaskTemplate.ContainerSpec.Configs {
		target := c.File.Name
		if target == "" {
			continue
		}
		if !strings.HasPrefix(target, "/") {
			target = "/" + target // Swarm's default mount root when the target is bare
		}
		// GET /configs/{id} returns ONE Config object, not a list (that is
		// GET /configs). Decoding it into a slice failed silently and the file
		// never came through - the e2e's Swarm leg caught it.
		var ci struct {
			Spec struct {
				Data string `json:"Data"`
			} `json:"Spec"`
		}
		if code, err := dockerAPI("GET", "/configs/"+c.ConfigID, nil, &ci); err != nil || code != 200 {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(ci.Spec.Data)
		if err != nil {
			continue
		}
		files[target] = data
		paths = append(paths, target)
	}
	tarball := tarFromFiles(files)
	// The secrets read at park time, if any: merge them in and add their mount
	// root to the paths so the client repoints the variables that name it.
	if stash, err := os.ReadFile(swarmSecretStash(name)); err == nil && len(stash) > 0 {
		tarball = mergeTars(tarball, stash)
		paths = append(paths, secretsMount)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	return paths, tarball
}

// swarmSecretStash is where a parked Swarm service's /run/secrets is kept. A
// Swarm secret is a file only inside a RUNNING task, and the takeover scales the
// service to zero, so it is read at park time and stashed here for files-of. Per
// name, on the agent's own filesystem, removed when the service is restored.
func swarmSecretStash(name string) string { return "/tmp/plug-secrets-" + name + ".tar" }

// swarmStashSecrets reads /run/secrets from a RUNNING task of the service about
// to be parked and writes it (rerooted onto its absolute path) to the stash.
// Best-effort by design: no running task reachable from this node (a multi-node
// swarm may schedule it elsewhere), no /run/secrets, or any error simply leaves
// no stash, and files-of hands over the configs alone. Called at park, before
// the scale-down, while a task is still up to read.
func swarmStashSecrets(name, service string) {
	filter := `{"service":["` + service + `"],"desired-state":["running"]}`
	var tasks []struct {
		Status struct {
			ContainerStatus struct {
				ContainerID string `json:"ContainerID"`
			} `json:"ContainerStatus"`
		} `json:"Status"`
	}
	if code, err := dockerAPI("GET", "/tasks?filters="+url.QueryEscape(filter), nil, &tasks); err != nil || code != 200 {
		return
	}
	for _, t := range tasks {
		id := t.Status.ContainerStatus.ContainerID
		if id == "" {
			continue
		}
		// A Swarm secret is a tmpfs mount, invisible to the archive API (which
		// reads the layer), so it is tarred from INSIDE the running task. -C /
		// makes the members "run/secrets/..." - absolute minus the leading slash,
		// the shape the client's untar expects, so no reroot. Needs `tar` in the
		// image; a distroless task yields nothing and files-of falls back to the
		// configs (same limit as env's `cat` on a distroless image).
		if tarball, err := dockerExec(id, []string{"tar", "-c", "-C", "/", strings.TrimPrefix(secretsMount, "/")}); err == nil && len(tarball) > 0 {
			_ = os.WriteFile(swarmSecretStash(name), tarball, 0o600)
			return
		}
	}
}

// mergeTars concatenates the entries of two tars into one. Either may be empty;
// the result carries every regular file and directory of both, which is how a
// Swarm reply combines its configs (built here) with its secrets (stashed at
// park) into the single tar files-of returns.
func mergeTars(a, b []byte) []byte {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, in := range [][]byte{a, b} {
		if len(in) == 0 {
			continue
		}
		tr := tar.NewReader(bytes.NewReader(in))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				continue
			}
			if tw.WriteHeader(h) == nil {
				io.Copy(tw, tr)
			}
		}
	}
	tw.Close()
	return out.Bytes()
}

// rerootTar rewrites a tar produced for a single path (its members rooted at the
// path's basename, as the Docker archive API returns them) so each member sits
// on its ABSOLUTE path minus the leading slash - the shape `tar cf - /p` gives
// and the client's untar expects (it re-anchors under the temp dir). So archive
// of /run/secrets, whose members read "secrets/foo", comes back "run/secrets/foo".
func rerootTar(in []byte, dest string) []byte {
	prefix := strings.TrimPrefix(path.Dir(dest), "/") // "/run/secrets" -> "run"
	tr := tar.NewReader(bytes.NewReader(in))
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return in // unreadable: hand it back untouched rather than lose it
		}
		h.Name = path.Join(prefix, h.Name)
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeDir {
			tw.WriteHeader(h)
			io.Copy(tw, tr)
		}
	}
	tw.Close()
	return out.Bytes()
}

// tarFromFiles builds a tar whose members are the given absolute paths (leading
// slash dropped, as tar stores them) with the given contents - the Swarm path,
// where the agent has the bytes in hand rather than a tar to re-root.
func tarFromFiles(files map[string][]byte) []byte {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for p, data := range files {
		tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(p, "/"), Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	tw.Close()
	return out.Bytes()
}

// k8sFilesOf finds the running pod behind the parked Service, selects its
// projectable mounts, and tars them out of the container. It returns the paths
// and the tar; on any miss it returns an empty set rather than an error, since
// files are a best-effort addition to the environment, never the thing that
// fails a session.
func k8sFilesOf(ns, name string) ([]string, []byte) {
	var svc struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &svc); err != nil || code != 200 {
		return nil, nil
	}
	sel := svc.Spec.Selector
	if raw := svc.Metadata.Annotations[k8sParkedAnn]; raw != "" {
		var r k8sReceipt
		if json.Unmarshal([]byte(raw), &r) == nil && len(r.Selector) > 0 {
			sel = r.Selector
		}
	}
	if len(sel) == 0 {
		return nil, nil
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
			Spec struct {
				Volumes    []k8sVolume `json:"volumes"`
				Containers []struct {
					Name         string     `json:"name"`
					VolumeMounts []k8sMount `json:"volumeMounts"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods?labelSelector="+url.QueryEscape(labelSelector(sel)), nil, &pods); err != nil || code != 200 {
		return nil, nil
	}
	for _, p := range pods.Items {
		if p.Status.Phase != "Running" || len(p.Spec.Containers) == 0 {
			continue
		}
		paths := projectableMounts(p.Spec.Volumes, p.Spec.Containers[0].VolumeMounts)
		if len(paths) == 0 {
			return nil, nil
		}
		// -h DEREFERENCES symlinks: a Secret (and a projected ConfigMap) is
		// mounted through the kubelet's atomic-writer, so /run/plug-test/ca is a
		// symlink into a ..data/ directory, not a regular file. Without -h tar
		// stored the link, the client's untar skipped it (a link is the one
		// member that could escape the temp dir), and the file never landed - the
		// projection looked done and the value pointed at nothing. -h makes the
		// leaf a regular file with its content.
		tar, _, err := k8sExec(ns, p.Metadata.Name, p.Spec.Containers[0].Name, append([]string{"tar", "-c", "-h", "-f", "-"}, paths...)...)
		if err != nil {
			return nil, nil // no exec, or no tar (distroless): env-of already noted it
		}
		return paths, tar
	}
	return nil, nil
}
