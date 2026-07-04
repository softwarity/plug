package tunnel

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSplitResolver checks the classify→route→fallback logic against a real
// agent: single-label names go to the cluster, dotted names to the "host"
// upstream (8.8.8.8 here), each falling back to the other.
//
//	PLUG_TEST_KEY=... PLUG_TEST_AGENT=localhost:2222 go test ./internal/tunnel/ -run TestSplitResolver -v
func TestSplitResolver(t *testing.T) {
	tr := testTransport(t) // skips unless PLUG_TEST_AGENT is set

	r := NewResolver(tr, []string{"8.8.8.8:53"}, 0, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := r.Serve(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("udp", addr)
		},
	}

	cases := []struct {
		name      string
		wantCIDR  string // expected answer prefix (cluster subnet or public)
		mustError bool
	}{
		{name: "pdfbox", wantCIDR: "10.0."},        // single-label → cluster
		{name: "mongodb", wantCIDR: "10.0."},       // single-label → cluster
		{name: "github.com", wantCIDR: ""},         // dotted → host (any public IP)
		{name: "nope-not-a-real-host-xyz", mustError: true}, // single → cluster NXDOMAIN → host NXDOMAIN
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		addrs, err := res.LookupHost(ctx, c.name)
		cancel()
		if c.mustError {
			if err == nil {
				t.Errorf("%s: expected failure, got %v", c.name, addrs)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.wantCIDR != "" && !strings.HasPrefix(addrs[0], c.wantCIDR) {
			t.Errorf("%s → %v, want prefix %q", c.name, addrs, c.wantCIDR)
			continue
		}
		t.Logf("%-28s → %v", c.name, addrs)
	}
	_ = os.Getenv
}
