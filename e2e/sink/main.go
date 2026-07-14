// sink is the runner-side local service for the reverse-direction gateway e2e.
// Run under `plug -s <name>:<cport>:<lport>`, it becomes reachable inside the
// cluster as <name>:<cport>. It answers "<path> <id>" — the request PATH it was
// hit on plus the `id` query param — so a caller proves BOTH the path and the
// id travelled the tunnel intact (path "/" for a root call). A broken or
// wrong-path route can't fake this: only the real request reflects the exact
// path+id back through the gateway.
package main

import (
	"flag"
	"fmt"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18096", "listen address")
	flag.Parse()
	panic(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path+" "+r.URL.Query().Get("id"))
	})))
}
