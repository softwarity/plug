package tunnel

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests need a running agent and are skipped unless PLUG_TEST_AGENT is
// set to host:port. They exercise the real SSH direct-tcpip path:
//
//	PLUG_TEST_KEY=path/to/id_ed25519 PLUG_TEST_AGENT=localhost:12222 \
//	PLUG_TEST_SERVICE=web:80 PLUG_TEST_NAME=web go test ./internal/tunnel/ -v
func testTransport(t *testing.T) *Transport {
	t.Helper()
	addr := os.Getenv("PLUG_TEST_AGENT")
	if addr == "" {
		t.Skip("set PLUG_TEST_AGENT=host:port to run transport tests")
	}
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("PLUG_TEST_AGENT must be host:port, got %q", addr)
	}
	key, err := os.ReadFile(os.Getenv("PLUG_TEST_KEY"))
	if err != nil {
		t.Fatalf("PLUG_TEST_KEY: %v", err)
	}
	tr, err := Dial(host, port, "plug", [][]byte{key}, "", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestDialCluster(t *testing.T) {
	svc := os.Getenv("PLUG_TEST_SERVICE") // e.g. web:80
	if svc == "" {
		t.Skip("set PLUG_TEST_SERVICE=host:port")
	}
	tr := testTransport(t)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: tr.DialContext,
		},
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get("http://" + svc + "/")
	if err != nil {
		t.Fatalf("GET through tunnel: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	t.Logf("reached %s via tunnel: %d, %q", svc, resp.StatusCode, strings.TrimSpace(string(body)))
}

func TestResolveClusterName(t *testing.T) {
	name := os.Getenv("PLUG_TEST_NAME") // e.g. web
	if name == "" {
		t.Skip("set PLUG_TEST_NAME=<a cluster service name>")
	}
	tr := testTransport(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addrs, err := tr.Resolver().LookupHost(ctx, name)
	if err != nil {
		t.Fatalf("resolving %q over the tunnel: %v", name, err)
	}
	if len(addrs) == 0 {
		t.Fatalf("no address for %q", name)
	}
	t.Logf("resolved %s -> %v via cluster DNS over the tunnel", name, addrs)
}
