package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// dialFunc opens a connection to addr. For the real transport it is a
// direct-tcpip channel, so addr's host is resolved cluster-side.
type dialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// ServeHTTPProxy runs a local HTTP forward proxy whose every upstream connection
// goes into the cluster over the tunnel. Node's HTTP stack (axios,
// follow-redirects, undici/fetch) talks to an HTTP proxy but chokes on a SOCKS
// one — `assert socks5h: == http:` — so HTTP_PROXY / HTTPS_PROXY point here.
// Cluster names resolve because each upstream dial is a direct-tcpip channel,
// opened and thus resolved inside the cluster.
//
// It sends its bound address on ready, then serves until ctx is cancelled.
func (t *Transport) ServeHTTPProxy(ctx context.Context, listenAddr string, logf Logf, ready chan<- string) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if ready != nil {
		ready <- ln.Addr().String()
	}
	return serveHTTPProxy(ctx, ln, t.DialContext, logf)
}

// serveHTTPProxy serves the forward proxy on ln, dialing every upstream through
// dial, until ctx is cancelled. Split from the Transport method so it can be
// tested against any dial without an SSH connection.
func serveHTTPProxy(ctx context.Context, ln net.Listener, dial dialFunc, logf Logf) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	// Upstream transport: it hands DialContext the unresolved host:port, so the
	// name is resolved cluster-side.
	rt := &http.Transport{
		DialContext:         dial,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxyConnect(dial, w, r, logf)
			} else {
				proxyForward(rt, w, r, logf)
			}
		}),
	}
	err := srv.Serve(ln)
	if ctx.Err() != nil {
		return nil // listener closed on shutdown, not an error
	}
	return err
}

// proxyConnect handles CONNECT (HTTPS tunnelling): open a channel to the target
// and splice the two ends together, so TLS stays end-to-end.
func proxyConnect(dial dialFunc, w http.ResponseWriter, r *http.Request, logf Logf) {
	remote, err := dial(r.Context(), "tcp", withPort(r.Host, "443"))
	if err != nil {
		http.Error(w, "cannot reach "+r.Host, http.StatusBadGateway)
		logf("http-proxy CONNECT %s: %v", r.Host, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		remote.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		remote.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		remote.Close()
		return
	}
	relay(client, remote)
}

// proxyForward handles plain-HTTP proxy requests (absolute-form GET/POST/…): send
// them upstream through the tunnel and copy the response back.
func proxyForward(rt *http.Transport, w http.ResponseWriter, r *http.Request, logf Logf) {
	if !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "plug http-proxy: expected a proxy request", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = "" // RoundTrip rejects a request that still has RequestURI set
	stripHopHeaders(out.Header)
	resp, err := rt.RoundTrip(out)
	if err != nil {
		http.Error(w, "cannot reach "+r.URL.Host, http.StatusBadGateway)
		logf("http-proxy %s %s: %v", r.Method, r.URL.Host, err)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	dst := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// withPort appends defPort to hostport when it carries no port.
func withPort(hostport, defPort string) string {
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		return net.JoinHostPort(hostport, defPort)
	}
	return hostport
}

// stripHopHeaders drops the hop-by-hop headers that must not cross a proxy.
func stripHopHeaders(h http.Header) {
	for _, k := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		h.Del(k)
	}
}
