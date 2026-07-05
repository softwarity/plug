// A trivial HTTP backend that answers a marker, so a redirected connect is
// unambiguously visible.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("BACKEND_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "SECCOMP-REDIRECT-OK")
	})
	fmt.Printf("[backend] listening on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("[backend] %v\n", err)
		os.Exit(1)
	}
}
