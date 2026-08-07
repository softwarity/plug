// chaos is the agent-crash simulator for the mesh e2e clusters, one image for
// all three families: GET :8095/restart-agent restarts the AGENT (a container
// through the mounted docker.sock, or a pod through the Kubernetes API), so the
// resilience cell can
// reproduce "the agent died mid-session" and assert the whole recovery chain —
// keepalive detects the dead transport, the agent's boot-gc restores what was
// parked, the reconnect re-arms -s and re-parks. The reply is sent BEFORE the
// restart fires (async, after a beat): the caller reaches us THROUGH the very
// agent about to die, so a synchronous restart would eat the answer.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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

const sock = "/var/run/docker.sock"

// onK8s reports whether we run inside a cluster rather than beside a Docker
// socket. Set for every pod by the kubelet, so it needs no configuration.
func onK8s() bool { return os.Getenv("KUBERNETES_SERVICE_HOST") != "" }

const k8sSA = "/var/run/secrets/kubernetes.io/serviceaccount"

// k8sDo performs one Kubernetes API call with the pod's service-account token.
func k8sDo(method, path string) (int, []byte, error) {
	token, err := os.ReadFile(k8sSA + "/token")
	if err != nil {
		return 0, nil, err
	}
	pool := x509.NewCertPool()
	if ca, cerr := os.ReadFile(k8sSA + "/ca.crt"); cerr == nil {
		pool.AppendCertsFromPEM(ca)
	}
	c := &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	req, err := http.NewRequest(method, "https://kubernetes.default.svc"+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// k8sNamespace is the namespace this pod runs in.
func k8sNamespace() string {
	ns, err := os.ReadFile(k8sSA + "/namespace")
	if err != nil {
		return "default"
	}
	return strings.TrimSpace(string(ns))
}

// agentNamespace says WHERE to look for the agent named svc.
//
// chaos answers in `default`, because that is where a session reaches it by
// name — but the per-leg crash-test agents live one namespace each, so that
// three OS legs crashing agents concurrently cannot tear down each other's
// sessions. They keep the label `app: plug` (their own self-update looks for
// it, and the update cells drive them), so they cannot be told apart by label:
// the fixture naming is the mapping, and it is a fixture, so that is fair —
// res-agent-<leg> is deployed in plug-res-<leg>. Anything else is looked for
// here, which is what the main agent needs.
func agentNamespace(svc string) string {
	if leg, ok := strings.CutPrefix(svc, "res-agent-"); ok {
		return "plug-res-" + leg
	}
	return k8sNamespace()
}

// k8sRestartAgent deletes the agent's pod. The Deployment recreates it, which
// is what "the agent died mid-session" looks like on Kubernetes — and unlike a
// container restart it also exercises a genuinely new pod, so the boot-gc runs
// exactly as it would after a node event.
func k8sRestartAgent(svc string) error {
	ns := agentNamespace(svc)
	code, body, err := k8sDo("GET", "/api/v1/namespaces/"+ns+"/pods?labelSelector=app%3D"+url.QueryEscape(svc))
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("listing pods app=%s in %s: HTTP %d", svc, ns, code)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return fmt.Errorf("no pod labelled app=%s in namespace %s", svc, ns)
	}
	go func(name string) {
		time.Sleep(500 * time.Millisecond) // let the reply drain through the tunnel
		_, _, _ = k8sDo("DELETE", "/api/v1/namespaces/"+ns+"/pods/"+name)
	}(list.Items[0].Metadata.Name)
	return nil
}

// docker performs one Docker Engine API call over the unix socket.
func docker(method, path string) (*http.Response, error) {
	c := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 20 * time.Second,
	}
	req, err := http.NewRequest(method, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// dockerBody is docker() with a JSON body — the exec API needs one, and the
// plain helper above deliberately has none.
func dockerBody(method, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	c := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 20 * time.Second,
	}
	req, err := http.NewRequest(method, "http://docker"+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

// dockerExec runs cmd inside a container and returns once it has been started.
func dockerExec(id string, cmd []string) error {
	resp, err := dockerBody("POST", "/containers/"+id+"/exec",
		map[string]any{"Cmd": cmd, "AttachStdout": false, "AttachStderr": false})
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"Id"`
	}
	derr := json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if derr != nil {
		return derr
	}
	if created.ID == "" {
		return fmt.Errorf("exec create returned no id")
	}
	start, err := dockerBody("POST", "/exec/"+created.ID+"/start", map[string]any{"Detach": true})
	if err != nil {
		return err
	}
	start.Body.Close()
	return nil
}

// agentID finds a running agent container by the labels its orchestrator gave
// it. Both schemes are scoped to the e2e stack, so a same-named service from
// anything else on the host can never be the target. svc selects WHICH agent
// (the per-leg crash-test ones, or the main one by default).
//
// Two schemes because the same chaos image serves both families: Compose labels
// a container with its service and project, Swarm labels a task with the
// stack-qualified service name. Trying Compose first and Swarm second means a
// cluster only ever matches its own.
func agentID(svc string) (string, error) {
	for _, label := range []string{
		`"com.docker.compose.service=` + svc + `","com.docker.compose.project=plug-e2e"`,
		`"com.docker.swarm.service.name=plug-e2e_` + svc + `"`,
	} {
		resp, err := docker("GET", "/containers/json?filters="+url.QueryEscape(`{"label":[`+label+`]}`))
		if err != nil {
			return "", err
		}
		var out []struct {
			ID string `json:"Id"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if derr != nil {
			return "", derr
		}
		if len(out) > 0 {
			return out[0].ID, nil
		}
	}
	return "", fmt.Errorf("no running container for agent %q under compose or swarm labels", svc)
}

func main() {
	mux := http.NewServeMux()
	// /resolve?name=X — what a WORKLOAD in this cluster gets when it looks the
	// name up. Asked from in here rather than from the harness because that is
	// the only place the answer means anything: the address callers cache and
	// keep using is the cluster's, not the runner's.
	//
	// Go's resolver holds no cache of its own, so each call is a fresh question
	// to the cluster's DNS — which is what makes "same address before and after"
	// a statement about the cluster rather than about this process.
	mux.HandleFunc("/resolve", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name= required", http.StatusBadRequest)
			return
		}
		addrs, err := net.DefaultResolver.LookupHost(r.Context(), name)
		if err != nil {
			fmt.Fprintf(w, "unresolved: %v\n", err)
			return
		}
		fmt.Fprintln(w, strings.Join(addrs, ","))
	})
	mux.HandleFunc("/restart-agent", func(w http.ResponseWriter, r *http.Request) {
		svc := r.URL.Query().Get("svc")
		if svc == "" {
			svc = "agent"
		}
		if onK8s() {
			// The delete is scheduled inside, after the reply — same reason as below.
			if err := k8sRestartAgent(svc); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			fmt.Fprint(w, "restarting")
			return
		}
		id, err := agentID(svc)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprint(w, "restarting") // answer FIRST — the path back dies with the agent
		go func() {
			time.Sleep(500 * time.Millisecond) // let the reply drain through the tunnel
			if resp, err := docker("POST", "/containers/"+id+"/restart?t=0"); err == nil {
				resp.Body.Close()
			}
		}()
	})
	// /kill-sessions?svc=<agent> drops every SSH SESSION on an agent while
	// leaving the agent itself running — the transport dies under live clients,
	// their keepalive notices, they reconnect and re-provision their names.
	//
	// That is the reconnect nobody could produce until now, and the one that
	// matters most: a laptop waking, a VPN switching, a Docker Desktop hiccup.
	// /restart-agent cannot stand in for it — a restarted agent runs its boot gc,
	// which sweeps its own signposts, so the address is legitimately lost and
	// the signpost REUSE (which keeps a Swarm VIP) never gets a chance to fire.
	//
	// sshd's listener is pid 1 and shows as "sshd: /usr/sbin/sshd … [listener]";
	// each session is a separate "sshd-session:" process. Matching the latter
	// leaves the former alone — the agent keeps accepting new connections
	// throughout, which is exactly what makes this a transport blip and not an
	// outage.
	// /agent-state?svc=<agent> — WHY an agent is not answering, instead of the
	// cell reporting that it is not.
	//
	// The resilience cell restarts an agent by design and then waits for it. When
	// it never came back the cell said exactly that and nothing else, so three
	// red cells (the restore, then both update cells, which use that same agent)
	// pointed at one invisible cause. Twice we guessed at timeouts; the honest
	// first move is to look. Container state plus its last lines say whether it
	// crashed, is restarting, or simply took longer than we waited.
	mux.HandleFunc("/agent-state", func(w http.ResponseWriter, r *http.Request) {
		svc := r.URL.Query().Get("svc")
		if svc == "" {
			svc = "agent"
		}
		if onK8s() {
			http.Error(w, "agent-state is docker/swarm only (a pod's state is kubectl's job)", 501)
			return
		}
		id, err := agentID(svc)
		if err != nil {
			http.Error(w, "no container for "+svc+": "+err.Error(), 500)
			return
		}
		resp, err := docker("GET", "/containers/"+id+"/json")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var insp struct {
			State struct {
				Status     string `json:"Status"`
				Running    bool   `json:"Running"`
				Restarting bool   `json:"Restarting"`
				ExitCode   int    `json:"ExitCode"`
				Error      string `json:"Error"`
				StartedAt  string `json:"StartedAt"`
				FinishedAt string `json:"FinishedAt"`
			} `json:"State"`
			RestartCount int `json:"RestartCount"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&insp)
		resp.Body.Close()
		if derr != nil {
			http.Error(w, derr.Error(), 500)
			return
		}
		fmt.Fprintf(w, "status=%s running=%v restarting=%v exit=%d restarts=%d started=%s finished=%s\n",
			insp.State.Status, insp.State.Running, insp.State.Restarting,
			insp.State.ExitCode, insp.RestartCount, insp.State.StartedAt, insp.State.FinishedAt)
		if insp.State.Error != "" {
			fmt.Fprintf(w, "error=%s\n", insp.State.Error)
		}
		// Its own last words. Docker multiplexes stdout/stderr with an 8-byte
		// header per frame when there is no TTY; strip it rather than print the
		// control bytes into a CI log.
		lg, err := docker("GET", "/containers/"+id+"/logs?stdout=1&stderr=1&tail=25")
		if err != nil {
			return
		}
		defer lg.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(lg.Body, 64<<10))
		fmt.Fprint(w, "--- last lines ---\n"+stripDockerFrames(raw))
	})

	mux.HandleFunc("/kill-sessions", func(w http.ResponseWriter, r *http.Request) {
		svc := r.URL.Query().Get("svc")
		if svc == "" {
			svc = "agent"
		}
		if onK8s() {
			// Exec into a pod needs SPDY/websocket, not the plain REST this
			// service speaks. Say so plainly — the cell reports "not
			// measurable" rather than silently proving nothing.
			http.Error(w, "kill-sessions is docker/swarm only (pod exec needs SPDY)", 501)
			return
		}
		id, err := agentID(svc)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprint(w, "killing") // answer FIRST — this request rides a session too
		go func() {
			time.Sleep(500 * time.Millisecond) // let the reply drain through the tunnel
			_ = dockerExec(id, []string{"pkill", "-f", "sshd-session"})
		}()
	})
	// /rm-signpost?name=<n> deletes the signpost a LIVE session created, without
	// touching the session itself. That is precisely the state a rebooted
	// agent's boot gc leaves behind — session alive, signpost gone — and the
	// only way to test that the name stays its owner's. Name ownership used to
	// be read off the signpost, so "no signpost" meant "free".
	mux.HandleFunc("/rm-signpost", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name required", 400)
			return
		}
		if onK8s() {
			// On Kubernetes the signpost is not a sidecar object: the agent
			// creates (or repoints) a Service CALLED the name itself, so that is
			// what has to go for the session to be left without one.
			code, _, err := k8sDo("DELETE", "/api/v1/namespaces/"+k8sNamespace()+"/services/"+name)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if code >= 300 {
				http.Error(w, fmt.Sprintf("deleting Service %s: HTTP %d", name, code), 404)
				return
			}
			fmt.Fprint(w, "removed")
			return
		}
		sp, gone := "plug-sp-"+name, false
		// Swarm shape first, then the standalone container one — a given cluster
		// has exactly one of them, and the other simply 404s.
		for _, path := range []string{"/services/" + sp, "/containers/" + sp + "?force=1"} {
			resp, err := docker("DELETE", path)
			if err != nil {
				continue
			}
			if resp.StatusCode < 300 {
				gone = true
			}
			resp.Body.Close()
		}
		if !gone {
			http.Error(w, "no signpost for "+name, 404)
			return
		}
		fmt.Fprint(w, "removed")
	})
	panic(http.ListenAndServe(":8095", mux))
}

// stripDockerFrames removes the 8-byte stream header Docker puts in front of
// every log frame when the container has no TTY. Without this the CI log gets
// control bytes where the agent's own words should be.
func stripDockerFrames(b []byte) string {
	var out []byte
	for len(b) >= 8 {
		n := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		if n < 0 || n > len(b)-8 {
			out = append(out, b[8:]...)
			break
		}
		out = append(out, b[8:8+n]...)
		b = b[8+n:]
	}
	return string(out)
}
