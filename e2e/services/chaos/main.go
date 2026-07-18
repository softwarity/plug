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

// agentID finds the running agent container by its compose labels — service
// AND project, so a same-named service from another compose project on the
// host can never be the target.
func agentID() (string, error) {
	filters := url.QueryEscape(`{"label":["com.docker.compose.service=agent","com.docker.compose.project=plug-e2e"]}`)
	resp, err := docker("GET", "/containers/json?filters="+filters)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no running container labeled compose service=agent")
	}
	return out[0].ID, nil
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/restart-agent", func(w http.ResponseWriter, _ *http.Request) {
		id, err := agentID()
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
	panic(http.ListenAndServe(":8095", mux))
}
