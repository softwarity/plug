// A tiny *Go* binary that dials a BOGUS, unreachable IP over TCP and reads the
// reply. Go makes the connect() as a raw syscall (no libc) → the only way to
// intercept it is at the kernel boundary (seccomp). If this reads the backend's
// marker, the supervisor caught Go's connect() and redirected it.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	target := "10.99.99.99:1234" // bogus — nothing there; must be redirected
	if len(os.Args) > 1 {
		target = os.Args[1]
	}
	fmt.Printf("[gotarget] Go dialing %s (bogus — only a redirect can make this work)\n", target)

	c, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		fmt.Printf("[gotarget] dial FAILED: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	fmt.Fprintf(c, "GET / HTTP/1.0\r\nHost: x\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(c)
	fmt.Printf("[gotarget] reply: %s\n", string(b))
}
