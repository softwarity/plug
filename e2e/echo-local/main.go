// echo-local is the RUNNER-side service for the expose e2e: the leg runs it
// UNDER plug with -s, making it reachable from inside the cluster under a
// cluster DNS name. It just answers a fixed text so the prober can assert the
// reply really came from this runner.
package main

import (
	"flag"
	"fmt"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18086", "listen address")
	text := flag.String("text", "echo-local", "response body")
	flag.Parse()
	panic(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, *text)
	})))
}
