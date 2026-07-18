// prober lets the mesh e2e make a request FROM INSIDE the cluster: the expose
// test serves a runner-local port under a cluster name (plug -s), then asks the
// prober — a plain cluster workload — to fetch it, proving the whole reverse
// path: cluster DNS name → agent alias → sshd remote-forward → the session's
// tunnel → the runner's local service.
//
//	GET prober:8097/fetch?url=http://exposed-linux:18081/   → proxied body
package main

import (
	"io"
	"net/http"
	"time"
)

func main() {
	mux := http.NewServeMux()
	// A fresh connection per fetch: the prober witnesses where the NAME routes
	// NOW. With the default keep-alive pool it answers from a connection opened
	// BEFORE a switch — on k8s the takeover repoints the Service but the parked
	// pods stay up, so a pooled connection kept reaching the old pod and the
	// witness lied (the takeover cell's 'during' probe saw the deployed text).
	client := &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	mux.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			http.Error(w, "missing url", 400)
			return
		}
		resp, err := client.Get(url)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	panic(http.ListenAndServe(":8097", mux))
}
