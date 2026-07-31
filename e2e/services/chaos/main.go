// chaos is the agent-crash simulator for the mesh e2e cluster: GET
// :8095/restart-agent restarts the AGENT container (found by its compose
// service label through the mounted docker.sock), so the resilience cell can
// reproduce "the agent died mid-session" and assert the whole recovery chain —
// keepalive detects the dead transport, the agent's boot-gc restores what was
// parked, the reconnect re-arms -s and re-parks. The reply is sent BEFORE the
// restart fires (async, after a beat): the caller reaches us THROUGH the very
// agent about to die, so a synchronous restart would eat the answer.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

const sock = "/var/run/docker.sock"

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
