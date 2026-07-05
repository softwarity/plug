// A cluster-side HTTP service that answers a marker, so reaching it by name
// through plug is unambiguous.
package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "CLUSTER-OK")
	})
	http.ListenAndServe(":8080", nil)
}
