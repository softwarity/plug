// A minimal WebSocket echo server: it upgrades the HTTP request and echoes every
// frame back verbatim, so any language's WebSocket client can prove it reached us
// BY NAME through plug. WebSocket exercises a path no request/response protocol in
// this matrix does: an HTTP Upgrade followed by a long-lived, bidirectional frame
// loop on the same TCP connection.
package main

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

// e2e only: accept any Origin (clients connect by cluster name, no browser).
var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func echo(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	for {
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		if err := c.WriteMessage(mt, msg); err != nil {
			return
		}
	}
}

func main() {
	http.HandleFunc("/", echo)
	log.Println("websocket echo server on :8090")
	if err := http.ListenAndServe(":8090", nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
