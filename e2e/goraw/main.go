// A *Go* client that reaches a service by RAW TCP (net.Dial + manual HTTP) — NOT
// net/http, so it does NOT honor HTTP_PROXY. This tests the interception PATH
// (hook), which on Linux Go bypasses (raw syscalls + pure-Go resolver). It is
// expected to XFAIL on Linux until the seccomp supervisor lands.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	url := "web:8080"
	if len(os.Args) > 1 {
		url = strings.TrimPrefix(os.Args[1], "http://")
	}
	host := url
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	c, err := net.DialTimeout("tcp", host, 6*time.Second)
	if err != nil {
		fmt.Printf("goraw: dial %s FAILED: %v\n", host, err)
		os.Exit(1)
	}
	defer c.Close()
	name := strings.Split(host, ":")[0]
	fmt.Fprintf(c, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", name)
	c.SetReadDeadline(time.Now().Add(6 * time.Second))
	b, _ := io.ReadAll(c)
	fmt.Printf("goraw: %s\n", string(b))
}
