package agent

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// doEnvOf answers the environment of the workload under <name>, as "KEY=VALUE"
// lines: the one this session parked, or, for --env-of, any deployed one, read
// where it runs (the collectors below find a running workload by its name and
// a parked one by its receipt). Nothing there, or nothing readable, answers an
// empty body: the client then runs the command with the caller's environment
// alone, which is what every version before this one did. A refusal that
// names the missing right goes on stdout through envNote, so the session
// prints it once and goes on.
// doEnvOf answers as KEY=VALUE records plus "# " notes. nul picks the wire
// format: the newline-delimited legacy form (env-of), and the NUL-delimited
// form (env-ofz) that carries a MULTI-LINE value whole - a CA in PEM, a config
// blob - where the legacy form truncated it at the first newline, because the
// record separator and a value's own newline were the same byte. NUL cannot
// occur in an environment value (it terminates the C string) nor in a note, so
// it separates records unambiguously. A client too old to know env-ofz gets
// "unknown command" and falls back to env-of, its multi-line limitation intact.
func doEnvOf(cmd []string, nul bool) {
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
	answer("%s", formatEnvReply(envLines(raw), envNotes, nul))
}

// formatEnvReply frames the entries and notes for the wire. env-ofz: records
// separated by NUL, the notes as "# " records first, then KEY=VALUE - a byte a
// value can never contain, so a value with a newline survives. env-of: the
// legacy newline form, notes each on their own "# " line then the entries, and
// the reason a multi-line value is cut at its first newline there.
func formatEnvReply(entries, notes []string, nul bool) string {
	if nul {
		recs := make([]string, 0, len(notes)+len(entries))
		for _, n := range notes {
			recs = append(recs, "# "+n)
		}
		recs = append(recs, entries...)
		return strings.Join(recs, "\x00")
	}
	var b strings.Builder
	for _, n := range notes {
		b.WriteString("# " + n + "\n")
	}
	b.WriteString(strings.Join(entries, "\n"))
	return b.String()
}

// envNote is how a collector explains itself: a "# " record among the answer.
// COLLECTED, not printed, because the NUL form emits notes and entries in one
// framed reply; the legacy form prints them just before the entries. Each verb
// runs in its own process, so this package-level slice holds one command's
// notes and no other's. stdout, never stderr - the client reads the verb over
// an SSH session whose two streams are merged, so a note on stderr would land
// inside the variables and be read as one.
var envNotes []string

func envNote(format string, a ...any) { envNotes = append(envNotes, fmt.Sprintf(format, a...)) }

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
	// Containers: the ones the name resolves to. nameOwners lists RUNNING ones,
	// and the one this session parked is stopped: its id is in the receipt the
	// signpost carries, which is how the teardown finds it to start it again.
	// A Compose container is not named after its service (project-svc-1), so
	// the name itself finds nothing; the receipt is what finds it.
	var out []string
	seen := map[string]bool{}
	ids := []string{}
	var sp struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+signpostName(name)+"/json", nil, &sp); err == nil && code == 200 {
		for _, id := range strings.Split(sp.Config.Labels[parkedContainersLabel], ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
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
		envNote("note: %s: the Service could not be read (code %d, %v), so no environment was projected", name, code, err)
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
		envNote("note: %s: the Service has no selector and no parking receipt names one, so no pod to read", name)
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
		if code != 403 {
			envNote("note: %s: listing pods for selector %s failed (code %d, %v)", name, labelSelector(sel), code, err)
		}
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
		envNote("note: %s: no Running pod behind selector %s (%d pod(s) seen), so no environment to read", name, labelSelector(sel), len(pods.Items))
		return nil
	}
	if env, code, err := k8sExecEnviron(ns, pod, containers[0]); err == nil {
		return env
	} else if code == 403 {
		envNote("note: the agent may not exec into pods in %s, so %s gets its variables from the pod SPEC: "+
			"values that come from a Secret or ConfigMap arrive empty. Grant it: kubectl -n %s patch role plug-serve-names "+
			"--type=json -p '[{\"op\":\"add\",\"path\":\"/rules/-\",\"value\":{\"apiGroups\":[\"\"],\"resources\":[\"pods/exec\"],\"verbs\":[\"get\",\"create\"]}}]'", ns, name, ns)
	} else if strings.Contains(err.Error(), "executable file not found") {
		// A distroless image: no /bin/cat to run. Nothing to grant, nothing to
		// fix on the cluster; the spec is what there is, and the note says so.
		envNote("note: %s's image has no `cat` (distroless), so the running process cannot be read: %s gets its variables from the pod SPEC, "+
			"and values that come from a Secret or ConfigMap arrive empty", name, name)
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
//
// The handshake is written by hand, without a WebSocket library, so the
// dependency tree stays what it was; and the method is GET, because that is
// what an upgrade is. An RBAC detail cost a withdrawn tag here: the API server
// evaluates a GET on /exec as the verb `get` and a POST as `create`, kubectl's
// SPDY path is a POST, and the manifest had granted `create` alone. GET got
// 403 with the rule in place, POST got 405 (no SPDY here), the agent fell back
// to the pod spec, and a service whose password comes from a Secret started
// with none. Kubernetes' own `edit` role grants both verbs on pods/exec; so
// does the manifest now, and this stays a GET.
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
	host := "kubernetes.default.svc"
	path := "/api/v1/namespaces/" + ns + "/pods/" + pod + "/exec?" + q.Encode()

	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", host+":443", &tls.Config{RootCAs: pool, ServerName: host})
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Authorization: Bearer " + strings.TrimSpace(string(token)) + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Protocol: v4.channel.k8s.io\r\n" +
		"\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return nil, 0, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, resp.StatusCode, fmt.Errorf("exec handshake: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	stdout, err := readChannelFrames(br, 1)
	if err != nil && len(stdout) == 0 {
		return nil, 0, err
	}
	if len(stdout) == 0 {
		// 101 and then nothing on channel 1: the session opened and the
		// process wrote nothing we kept. Said, not swallowed - an empty
		// environment is not a thing a running container has.
		return nil, 0, fmt.Errorf("exec opened but stdout carried nothing (read error: %v)", err)
	}
	return procEnviron(stdout), 0, nil
}

// readChannelFrames reads WebSocket frames from a server (never masked) until
// the connection closes or a close frame arrives, and concatenates the payload
// of every binary/text frame whose first byte is the wanted channel. Pure over
// its reader, so the framing is proven on bytes rather than on a cluster.
//
// Channel 3 is the API server's own verdict on the exec, a JSON Status: it is
// where "executable file not found" lives when the target image has no `cat`,
// which hashicorp/http-echo, a distroless image, does not. An evening was
// spent on an exec that opened and carried nothing, because this channel was
// dropped on the floor. It is returned as the error now, so the note names it.
func readChannelFrames(r *bufio.Reader, channel byte) ([]byte, error) {
	var out, status []byte
	for {
		h0, err := r.ReadByte()
		if err != nil {
			return out, err
		}
		h1, err := r.ReadByte()
		if err != nil {
			return out, err
		}
		opcode := h0 & 0x0f
		masked := h1&0x80 != 0
		n := uint64(h1 & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return out, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return out, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(r, mask[:]); err != nil {
				return out, err
			}
		}
		if n > 16<<20 {
			return out, fmt.Errorf("exec: frame of %d bytes", n)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return out, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x8: // close
			return out, execStatusError(status)
		case 0x1, 0x2: // text, binary
			if len(payload) == 0 {
				continue
			}
			switch payload[0] {
			case channel:
				out = append(out, payload[1:]...)
			case 3:
				status = append(status, payload[1:]...)
			}
		}
	}
}

// execStatusError turns the channel-3 Status into an error when it says
// Failure, and nil when it says Success or said nothing.
func execStatusError(status []byte) error {
	if len(status) == 0 {
		return nil
	}
	var st struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(status, &st) != nil {
		return fmt.Errorf("exec ended with an unreadable status: %.200s", string(status))
	}
	if st.Status == "Success" {
		return nil
	}
	return fmt.Errorf("exec: %s", strings.TrimSpace(st.Message+" "+st.Reason))
}
