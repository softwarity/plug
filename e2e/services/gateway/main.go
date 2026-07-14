// gateway simulates the cluster's API gateway for the reverse-direction e2e.
// It listens on a PUBLISHED cluster port (reachable from outside the cluster,
// like the agent's ssh port). An external caller POSTs {service, port, id}; the
// gateway makes the IN-CLUSTER call GET http://<service>:<port>/?id=<id> and
// returns whatever that answers. When <service> is a name a dev serves with
// `plug -s`, the call lands on that dev's LOCAL sink — so a green result proves
// the whole downward path end to end, TRIGGERED FROM OUTSIDE the cluster and
// WITHOUT going through plug's upward tunnel.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type call struct {
	Service string `json:"service"`
	Port    string `json:"port"`
	Path    string `json:"path"` // optional: call <service>:<port>/<path>?id=… (default "/")
	ID      string `json:"id"`
}

func main() {
	client := &http.Client{Timeout: 8 * time.Second}
	http.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		var c call
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "bad json: "+err.Error(), 400)
			return
		}
		if c.Service == "" || c.Port == "" {
			http.Error(w, "service and port are required", 400)
			return
		}
		// <service>:<port>/<path>?id=<id> — path optional (empty → "/"). The
		// sink echoes the path it was hit on, so the caller proves the path
		// travelled the tunnel intact, not just the host:port.
		url := fmt.Sprintf("http://%s:%s/%s?id=%s", c.Service, c.Port, strings.TrimPrefix(c.Path, "/"), c.ID)
		resp, err := client.Get(url)
		if err != nil {
			http.Error(w, "calling "+c.Service+":"+c.Port+": "+err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	panic(http.ListenAndServe(":8100", nil))
}
