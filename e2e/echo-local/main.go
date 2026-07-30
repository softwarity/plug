// echo-local is the RUNNER-side service for the expose e2e: the leg runs it
// UNDER plug with -s, making it reachable from inside the cluster under a
// cluster DNS name. It just answers a fixed text so the prober can assert the
// reply really came from this runner.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	// Comma-separated, index-matched: ONE process listening on several ports,
	// each answering its own text — how the multiport cell asserts that one
	// name's cluster ports route to the right local listener and not to each
	// other.
	addr := flag.String("addr", "127.0.0.1:18086", "listen address(es), comma-separated")
	text := flag.String("text", "echo-local", "response body per address, comma-separated")
	// -ttl lets a cell end the plug session NATURALLY (child exits → plug tears
	// down and restores) instead of `kill`, which on Windows/Git Bash is a brutal
	// TerminateProcess that never runs the teardown — the takeover cell needs the
	// restore path to actually execute on all three OSes.
	ttl := flag.Duration("ttl", 0, "exit 0 after this long (0 = run forever)")
	flag.Parse()
	if *ttl > 0 {
		time.AfterFunc(*ttl, func() { os.Exit(0) })
	}
	addrs := strings.Split(*addr, ",")
	texts := strings.Split(*text, ",")
	for i, ad := range addrs {
		t := texts[0]
		if i < len(texts) {
			t = texts[i]
		}
		go func(ad, t string) {
			panic(http.ListenAndServe(ad, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, t)
			})))
		}(ad, t)
	}
	select {}
}
