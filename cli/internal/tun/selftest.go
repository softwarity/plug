package tun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"time"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// loopbackDialer is a Dialer that ignores the cluster name and connects to a
// fixed local address — the pretend "cluster service" for the self-test.
type loopbackDialer struct{ addr string }

func (d loopbackDialer) DialCluster(string) (net.Conn, error) {
	return net.DialTimeout("tcp", d.addr, 5*time.Second)
}

// SelfTest exercises the entire TUN data path on THIS OS with no SSH agent and
// no Docker: it stands up a loopback echo server as the fake cluster service,
// brings up the real TUN + netstack + DNS, resolves a name through plug's own
// resolver, then dials the minted fake IP and checks bytes round-trip through
// the device. It is the cross-platform proof that the privileged path (create
// device + routes) and the datapath work natively on macOS / Windows / Linux.
func SelfTest(logf func(string, ...any)) error {
	log := logfn(logf)
	if err := checkPriv(); err != nil {
		return err
	}

	// The pretend cluster service: a loopback TCP echo server.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("echo listen: %w", err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	_, echoPort, _ := net.SplitHostPort(echo.Addr().String())

	dev, err := wgtun.CreateTUN(defaultTUNName, mtu)
	if err != nil {
		return fmt.Errorf("create TUN (need root/admin): %w", err)
	}
	defer dev.Close()
	ifname, _ := dev.Name()

	upstreams, cleanup, err := configure(ifname, dnsAddr, log)
	if err != nil {
		return fmt.Errorf("configure %s: %w", ifname, err)
	}
	defer cleanup()

	tab := newFaketab()
	dnsConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(dnsAddr), Port: 53})
	if err != nil {
		return fmt.Errorf("dns %s:53: %w", dnsAddr, err)
	}
	defer dnsConn.Close()
	go serveDNS(dnsConn, tab, upstreamResolver(upstreams), log)

	st, ep := buildStack(tab, loopbackDialer{addr: echo.Addr().String()}, log)
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &bridge{dev: dev, ep: ep}
	go br.toStack()
	go br.fromStack(ctx)
	log.f("selftest: TUN %s up, echo at %s", ifname, echo.Addr())

	// Resolve a cluster name through plug's own resolver (dial :53 directly, so
	// this is portable — macOS/Windows don't consult /etc/resolv.conf for Go).
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(dnsAddr, "53"))
	}}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	ips, err := res.LookupIP(rctx, "ip4", "selftest")
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("resolve fake name: %w", err)
	}
	fake := ips[0].String()
	log.f("selftest: name selftest → fake %s, dialing %s:%s through the TUN", fake, fake, echoPort)

	// Dial the fake IP: the OS routes it into the TUN, the netstack accepts it,
	// maps it back to the name, and the loopback dialer returns the echo server.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(fake, echoPort), 8*time.Second)
	if err != nil {
		return fmt.Errorf("dial through TUN: %w", err)
	}
	defer conn.Close()

	want := []byte("plug-selftest-42")
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := conn.Write(want); err != nil {
		return fmt.Errorf("write through TUN: %w", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("read through TUN: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("round-trip mismatch: got %q want %q", got, want)
	}
	log.f("selftest: %d bytes round-tripped through %s by name — OK", len(want), ifname)
	return nil
}
