package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

// doEnvOf answers the environment of the workload parked under <name>, as
// "KEY=VALUE" lines. Nothing parked, or nothing readable, answers an empty
// body: the client then runs the command with the caller's environment alone,
// which is what every version before this one did. A refusal that names the
// missing right goes on stderr through envNote, so the session prints it once
// and goes on.
func doEnvOf(cmd []string) {
	if len(cmd) != 2 || !nameRe.MatchString(cmd[1]) {
		answer("error: usage: env-of <name>")
	}
	name := cmd[1]
	var raw []string
	switch {
	case k8sAvailable():
		raw = k8sEnvOf(k8sNamespace(), name)
	case dockerAvailable():
		raw = dockerEnvOf(name)
	}
	answer("%s", strings.Join(envLines(raw), "\n"))
}

// envNote is how a collector explains an empty answer: one line on stderr,
// which the client relays as a note rather than a failure.
func envNote(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

// dockerEnvOf reads Config.Env of the containers the name resolves to, which
// docker hands back already resolved (Compose has substituted its ${VAR} and
// env_file by then) and hands back for a STOPPED container too - the parked
// one. On Swarm the service spec carries the same list. Secrets are not in
// either: Swarm mounts them under /run/secrets, and that is where they stay.
func dockerEnvOf(name string) []string {
	self, err := dockerSelf()
	if err != nil {
		return nil
	}
	if self.service != "" && swarmManager() {
		if own := swarmNameOwner(name, self); own != nil {
			var s struct {
				Spec struct {
					TaskTemplate struct {
						ContainerSpec struct {
							Env []string `json:"Env"`
						} `json:"ContainerSpec"`
					} `json:"TaskTemplate"`
				} `json:"Spec"`
			}
			if _, err := dockerAPI("GET", "/services/"+own.id, nil, &s); err == nil {
				return s.Spec.TaskTemplate.ContainerSpec.Env
			}
		}
	}
	// Containers: the ones the name resolves to, running or parked. nameOwners
	// lists running ones; a parked one is stopped, so look it up by name too.
	var out []string
	seen := map[string]bool{}
	ids := []string{}
	for _, o := range nameOwners(name, self.attachableNets()) {
		ids = append(ids, o.id)
	}
	ids = append(ids, name)
	for _, id := range ids {
		var insp struct {
			ID     string `json:"Id"`
			Config struct {
				Env    []string          `json:"Env"`
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}
		if code, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp); err != nil || code != 200 {
			continue
		}
		if seen[insp.ID] || insp.Config.Labels[signpostLabel] == "1" {
			continue
		}
		seen[insp.ID] = true
		out = append(out, insp.Config.Env...)
	}
	return out
}

// k8sEnvOf reads the environment of the pods behind the parked Service: the
// Service's original selector is in the parking receipt, the pods are still
// running (a takeover repoints the Service, it does not scale the Deployment),
// and `exec cat /proc/1/environ` in the first of them returns what the process
// actually has - Secrets included, resolved by the pod, with no right to read
// Secrets asked for. That is mirrord's mechanism.
//
// Without pods/exec the spec is the fallback: the same variables, except that
// a valueFrom (a Secret or ConfigMap reference) has no value in it. Those are
// named on stderr so the session says which ones came through empty and what
// right would fill them.
func k8sEnvOf(ns, name string) []string {
	var svc struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &svc); err != nil || code != 200 {
		return nil
	}
	sel := svc.Spec.Selector
	if raw := svc.Metadata.Annotations[k8sParkedAnn]; raw != "" {
		var r k8sReceipt
		if json.Unmarshal([]byte(raw), &r) == nil && len(r.Selector) > 0 {
			sel = r.Selector // the ORIGINAL selector: the parked Service points at the agent now
		}
	}
	if len(sel) == 0 {
		return nil
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
				Containers []struct {
					Name string `json:"name"`
					Env  []struct {
						Name      string          `json:"name"`
						Value     string          `json:"value"`
						ValueFrom json.RawMessage `json:"valueFrom"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods?labelSelector="+url.QueryEscape(labelSelector(sel)), nil, &pods)
	if err != nil || code != 200 {
		if code == 403 {
			envNote("note: the agent may not list pods in %s, so %s starts with your environment alone. "+
				"Grant it: kubectl -n %s patch role plug-serve-names --type=json -p '[{\"op\":\"add\",\"path\":\"/rules/-\",\"value\":{\"apiGroups\":[\"\"],\"resources\":[\"pods\"],\"verbs\":[\"get\",\"list\"]}}]'", ns, name, ns)
		}
		return nil
	}
	var pod string
	var containers []string
	for _, p := range pods.Items {
		if p.Status.Phase == "Running" {
			pod = p.Metadata.Name
			for _, c := range p.Spec.Containers {
				containers = append(containers, c.Name)
			}
			break
		}
	}
	if pod == "" {
		return nil
	}
	if env, code, err := k8sExecEnviron(ns, pod, containers[0]); err == nil {
		return env
	} else if code == 403 {
		envNote("note: the agent may not exec into pods in %s, so %s gets its variables from the pod SPEC: "+
			"values that come from a Secret or ConfigMap arrive empty. Grant it: kubectl -n %s patch role plug-serve-names "+
			"--type=json -p '[{\"op\":\"add\",\"path\":\"/rules/-\",\"value\":{\"apiGroups\":[\"\"],\"resources\":[\"pods/exec\"],\"verbs\":[\"create\"]}}]'", ns, name, ns)
	} else {
		envNote("note: reading %s's environment from pod %s failed (%v); using the pod spec instead", name, pod, err)
	}
	// The spec fallback.
	var out, missing []string
	for _, p := range pods.Items {
		if p.Metadata.Name != pod {
			continue
		}
		for _, e := range p.Spec.Containers[0].Env {
			if len(e.ValueFrom) > 0 {
				missing = append(missing, e.Name)
				out = append(out, e.Name+"=")
				continue
			}
			out = append(out, e.Name+"="+e.Value)
		}
	}
	if len(missing) > 0 {
		envNote("note: %s: empty because they come from a Secret or ConfigMap: %s", name, strings.Join(missing, ", "))
	}
	return out
}

// netDialer bounds the exec handshake the way k8sClient bounds a request.
var netDialer = net.Dialer{Timeout: 10 * time.Second}

func labelSelector(sel map[string]string) string {
	parts := make([]string, 0, len(sel))
	for k, v := range sel {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// k8sExecEnviron runs `cat /proc/1/environ` in the container over the exec
// subresource, which is a session rather than a request: the API server opens
// a connection to the kubelet and relays the process's streams over it,
// multiplexed by channel. WebSocket carries that since Kubernetes 1.31 (SPDY,
// a pre-HTTP/2 protocol nobody else kept, before that); one byte of channel id
// leads each frame, and channel 1 is stdout. stdout only, no stdin, no tty:
// the smallest shape of the protocol.
func k8sExecEnviron(ns, pod, container string) ([]string, int, error) {
	token, err := os.ReadFile(k8sSA + "/token")
	if err != nil {
		return nil, 0, err
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(k8sSA + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	q := url.Values{}
	q.Set("container", container)
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	q.Add("command", "cat")
	q.Add("command", "/proc/1/environ")
	loc := "wss://kubernetes.default.svc/api/v1/namespaces/" + ns + "/pods/" + pod + "/exec?" + q.Encode()
	cfg, err := websocket.NewConfig(loc, "https://kubernetes.default.svc")
	if err != nil {
		return nil, 0, err
	}
	cfg.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	cfg.Protocol = []string{"v4.channel.k8s.io"}
	cfg.TlsConfig = &tls.Config{RootCAs: pool}
	cfg.Dialer = &netDialer
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		// A 403 surfaces as a handshake error carrying the status: name it as
		// such so the caller can say which right is missing.
		if strings.Contains(err.Error(), "403") {
			return nil, 403, err
		}
		return nil, 0, err
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var stdout []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := ws.Read(buf)
		if n > 1 && buf[0] == 1 {
			stdout = append(stdout, buf[1:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			if len(stdout) > 0 {
				break
			}
			return nil, 0, err
		}
	}
	return procEnviron(stdout), 0, nil
}
