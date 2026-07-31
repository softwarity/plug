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

// k8sNamespace is the namespace this pod runs in — the per-leg crash-test
// agents each live in their own, so chaos must act where it is deployed.
func k8sNamespace() string {
	ns, err := os.ReadFile(k8sSA + "/namespace")
	if err != nil {
		return "default"
	}
	return strings.TrimSpace(string(ns))
}

// k8sRestartAgent deletes the agent's pod. The Deployment recreates it, which
// is what "the agent died mid-session" looks like on Kubernetes — and unlike a
// container restart it also exercises a genuinely new pod, so the boot-gc runs
// exactly as it would after a node event.
func k8sRestartAgent(svc string) error {
	ns := k8sNamespace()
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
