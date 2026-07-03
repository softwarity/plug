package main

import (
	"encoding/json"
	"net"
)

// socketPath is the unix socket the root daemon listens on. The CLI (running as
// your user) talks to it so day-to-day use never needs sudo.
const socketPath = "/var/run/plug.sock"

// msg is the single message type exchanged over the socket. The CLI sends a
// mount request; the daemon replies Ready (or Error), then holds the tunnel up
// until the CLI sends Done (or disconnects), then replies Closed.
type msg struct {
	Host    string   `json:"host,omitempty"`
	Port    string   `json:"port,omitempty"`
	Subnets []string `json:"subnets,omitempty"`

	Ready  bool   `json:"ready,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// runViaDaemon asks the root daemon to mount the TUN, runs the child command as
// the current user, then asks the daemon to tear the tunnel down. No sudo.
func runViaDaemon(cfg config, subnets []string, cmdArgs []string) int {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		info("no plug daemon running — install it once with:  sudo plug setup")
		return 1
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(msg{Host: cfg.host, Port: cfg.port, Subnets: subnets}); err != nil {
		info("daemon: %v", err)
		return 1
	}
	var rep msg
	if err := dec.Decode(&rep); err != nil {
		info("daemon: no response (%v)", err)
		return 1
	}
	if !rep.Ready {
		info("daemon: %s", rep.Error)
		return 1
	}
	info("tunnel up via daemon — routing %d subnet(s)", len(subnets))

	code := runChild(cmdArgs)

	// Ask for a synchronous teardown so DNS/routes are restored before we exit.
	enc.Encode(msg{Done: true})
	var end msg
	dec.Decode(&end)
	info("tunnel closed")
	return code
}
