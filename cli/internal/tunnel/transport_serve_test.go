package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// What this file replaces: three functions gated on PLUG_TEST_AGENT and friends,
// environment variables that nothing in the repository has ever set. A search
// across every workflow, script and Makefile found them only in that file. So
// they never ran, on any machine, in any pipeline. And had they run, they
// asserted nothing: one logged the status code it received and the other checked
// that a slice was non-empty. They stood in for coverage of the direct-tcpip
// path without providing any.
//
// These run everywhere, against an SSH server in this process that actually
// FORWARDS what it is asked to forward, so the bytes are real and the assertions
// are about them.

// servingAgent accepts direct-tcpip and splices each channel to a local address
// the test chose, recording what it was asked for. A real agent resolves that
// target in the cluster; here the test is the cluster.
type servingAgent struct {
	addr   string
	target string // where every forwarded channel actually goes

	mu   sync.Mutex
	asks []string // the host:port the client asked the CLUSTER for, in order
	// asked is signalled on every request, so a test can wait for the routing
	// DECISION instead of waiting for the exchange behind it to finish. A test
	// that waits for the answer is at the mercy of whatever is, or is not, at the
	// far end: the version of this that waited for LookupHost to return took
	// sixty seconds to fail and reported it as a test binary timeout with a
	// goroutine dump, rather than saying what was wrong.
	asked chan struct{}
}

func newServingAgent(t *testing.T, target string) *servingAgent {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &servingAgent{addr: ln.Addr().String(), target: target, asked: make(chan struct{}, 8)}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(signer)
	go a.serve(ln, cfg)
	t.Cleanup(func() { ln.Close() })
	return a
}

func (a *servingAgent) serve(ln net.Listener, cfg *ssh.ServerConfig) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
			if err != nil {
				c.Close()
				return
			}
			defer sconn.Close()
			go ssh.DiscardRequests(reqs)
			for nc := range chans {
				if nc.ChannelType() != "direct-tcpip" {
					nc.Reject(ssh.Prohibited, "this agent only forwards")
					continue
				}
				var req struct {
					Host     string
					Port     uint32
					OrigHost string
					OrigPort uint32
				}
				if err := ssh.Unmarshal(nc.ExtraData(), &req); err != nil {
					nc.Reject(ssh.ConnectionFailed, "unparsable forward request")
					continue
				}
				a.mu.Lock()
				a.asks = append(a.asks, fmt.Sprintf("%s:%d", req.Host, req.Port))
				a.mu.Unlock()
				select {
				case a.asked <- struct{}{}:
				default: // a test that stopped listening must never wedge the agent
				}

				ch, creqs, err := nc.Accept()
				if err != nil {
					continue
				}
				go ssh.DiscardRequests(creqs)
				go a.splice(ch)
			}
		}(c)
	}
}

func (a *servingAgent) splice(ch ssh.Channel) {
	defer ch.Close()
	up, err := net.Dial("tcp", a.target)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, ch); done <- struct{}{} }()
	go func() { io.Copy(ch, up); done <- struct{}{} }()
	<-done
}

func (a *servingAgent) asked_() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asks...)
}

func dialTo(t *testing.T, a *servingAgent) *Transport {
	t.Helper()
	host, port, _ := net.SplitHostPort(a.addr)
	tr, err := Dial(host, port, "plug", [][]byte{clientKeyPEM(t)}, "", nil)
	if err != nil {
		t.Fatalf("connecting to the test agent: %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

// The promise the whole product rests on: a name that exists only in the cluster
// is reachable, unchanged, from a process on this machine. Asserted on the bytes
// that come back, not on the fact that something happened.
func TestDialContextReachesTheNameItWasGiven(t *testing.T) {
	srv := httptestServer(t, "hello from the cluster")
	agent := newServingAgent(t, srv)
	tr := dialTo(t, agent)

	client := &http.Client{
		Transport: &http.Transport{DialContext: tr.DialContext},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("http://web:80/")
	if err != nil {
		t.Fatalf("GET through the tunnel: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d through the tunnel, want 200", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(body)); got != "hello from the cluster" {
		t.Errorf("body %q came back through the tunnel, want the service's own text", got)
	}
	// And the agent was asked for the name as the caller wrote it, not for
	// something resolved on this side first. That is the difference between a
	// tunnel and a proxy the application has to know about.
	if asks := agent.asked_(); len(asks) == 0 || asks[0] != "web:80" {
		t.Errorf("the agent was asked for %v, want the cluster name web:80 verbatim", asks)
	}
}

// The resolver must ask the CLUSTER's DNS, over the same tunnel. If it ever fell
// back to the host's resolver the lookup would succeed on some machines and fail
// on others, for names that only the cluster knows.
func TestTheResolverAsksTheClusterOverTheTunnel(t *testing.T) {
	// Nothing answers DNS at the far end, so the lookup never completes. What is
	// asserted is WHERE it went, which is the routing decision, and that is known
	// the moment the agent is asked to forward.
	agent := newServingAgent(t, "127.0.0.1:1")
	tr := dialTo(t, agent)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = tr.Resolver().LookupHost(ctx, "web")
	}()

	select {
	case <-agent.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolver never went through the tunnel: a cluster name would then resolve only on " +
			"a machine whose own resolver happens to know it, which is the whole thing plug exists to avoid")
	}
	if asks := agent.asked_(); asks[0] != clusterResolver {
		t.Errorf("the resolver dialled %q through the tunnel, want the cluster's own DNS at %s",
			asks[0], clusterResolver)
	}
}

// httptestServer returns the address of a local HTTP server answering body.
func httptestServer(t *testing.T, body string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}
