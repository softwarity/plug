// flaky is the outage simulator for the mesh e2e cluster: its SERVICE port
// (:8099) listens ONLY while switched on, so a test can reproduce "service
// unreachable, then it comes back" INSIDE one live plug session — the switch is
// flipped BY THE CLIENT, through plug, over the always-up control port (:8098):
//
//	GET flaky:8098/up    start answering on :8099 ("flaky-ok")
//	GET flaky:8098/down  stop again (boot state)
package main

import (
	"fmt"
	"net"
	"net/http"
	"sync"
)

func main() {
	var mu sync.Mutex
	var ln net.Listener // nil = service down (the boot state)

	ctl := http.NewServeMux()
	ctl.HandleFunc("/up", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if ln == nil {
			l, err := net.Listen("tcp", ":8099")
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			ln = l
			go func() {
				_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, "flaky-ok")
				}))
			}()
		}
		fmt.Fprint(w, "up")
	})
	ctl.HandleFunc("/down", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if ln != nil {
			_ = ln.Close()
			ln = nil
		}
		fmt.Fprint(w, "down")
	})
	panic(http.ListenAndServe(":8098", ctl))
}
