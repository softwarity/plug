package agent

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
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
	if !k8sAvailable() {
		answer("%s", filesReplyJSON(nil, nil))
	}
	answer("%s", filesReplyJSON(k8sFilesOf(k8sNamespace(), cmd[1])))
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
		tar, _, err := k8sExec(ns, p.Metadata.Name, p.Spec.Containers[0].Name, append([]string{"tar", "cf", "-"}, paths...)...)
		if err != nil {
			return nil, nil // no exec, or no tar (distroless): env-of already noted it
		}
		return paths, tar
	}
	return nil, nil
}
