package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// startTestProxy runs serveHTTPProxy on a random local port with the given dial
// and returns the proxy address; the dial stands in for the tunnel.
func startTestProxy(t *testing.T, dial dialFunc) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveHTTPProxy(ctx, ln, dial, func(string, ...any) {})
	return ln.Addr().String()
}

// dialTo returns a dial that ignores the requested name and always reaches addr,
// i.e. the "cluster" resolves every service name to that backend.
func dialTo(addr string) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
}

// TestHTTPProxyPlainForward: a plain-HTTP request through the proxy reaches the
// backend by cluster name (the case Node's axios needs).
func TestHTTPProxyPlainForward(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s%s", r.Host, r.URL.Path)
	}))
	defer backend.Close()

	proxyAddr := startTestProxy(t, dialTo(backend.Listener.Addr().String()))
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get("http://msg-report-service:3000/health")
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got, want := string(body), "hello msg-report-service:3000/health"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestHTTPProxyConnect: an HTTPS request through the proxy (CONNECT tunnelling)
// reaches a TLS backend, with TLS staying end-to-end.
func TestHTTPProxyConnect(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secure ok")
	}))
	defer backend.Close()

	proxyAddr := startTestProxy(t, dialTo(backend.Listener.Addr().String()))
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}

	resp, err := client.Get("https://msg-report-service:8443/secure")
	if err != nil {
		t.Fatalf("GET https via proxy CONNECT: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "secure ok" {
		t.Fatalf("got %d %q, want 200 \"secure ok\"", resp.StatusCode, body)
	}
}

func TestWithPort(t *testing.T) {
	cases := map[string]string{
		"my-service":      "my-service:443",
		"my-service:8443": "my-service:8443",
		"10.0.0.5":        "10.0.0.5:443",
	}
	for in, want := range cases {
		if got := withPort(in, "443"); got != want {
			t.Errorf("withPort(%q) = %q, want %q", in, got, want)
		}
	}
}
