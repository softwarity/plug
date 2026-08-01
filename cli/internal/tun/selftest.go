package tun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
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
// brings up the real TUN + netstack + DNS, resolves a name by querying the
// in-netstack resolver at dnsIP:53 THROUGH the TUN (exactly as the child would),
// then dials the minted fake IP and checks bytes round-trip through the device.
// It is the cross-platform proof that the privileged path (create device +
// routes + system DNS) and the datapath work natively on macOS / Windows / Linux.
func SelfTest(logf func(string, ...any)) error {
	log := logfn(logf)
	if err := checkPriv(); err != nil {
		return err
	}

	const n = 0
	cidr, dnsIP, base := instanceNet(n)

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

	upstreams, privResolv, cleanup, err := configure(dev, 0, ifname, cidr, dnsIP, log)
	if err != nil {
		return fmt.Errorf("configure %s: %w", ifname, err)
	}
	// The DNS repoint is the one piece of state that outlives this process, so a
	// Ctrl-C mid-test MUST still undo it. Run cleanup once, from either the normal
	// return path or a signal.
	var once sync.Once
	doCleanup := func() { once.Do(cleanup) }
	defer doCleanup()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		if _, ok := <-sigs; ok {
			doCleanup()
			os.Exit(130)
		}
	}()

	tab := newFaketab(base)

	st, ep := buildStack(tab, constDial(loopbackDialer{addr: echo.Addr().String()}), newUpstream(upstreams), nil, log)
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &bridge{dev: dev, ep: ep}
	go br.toStack()
	go br.fromStack(ctx)
	log.f("selftest: TUN %s up (DNS %s:53), echo at %s", ifname, dnsIP, echo.Addr())

	// Resolve a cluster name the REAL way: query dnsIP:53 THROUGH the TUN, so the
	// netstack UDP forwarder answers — the exact path a child's getaddrinfo takes.
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(dnsIP, "53"))
	}}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	ips, err := res.LookupIP(rctx, "ip4", "selftest")
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("resolve fake name through the TUN: %w", err)
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

	// On macOS, prove the REAL fix end to end: the SYSTEM resolver (getaddrinfo via
	// mDNSResponder — the path a real app takes, which ignores /etc/resolv.conf)
	// now resolves a single-label name to a fake IP. That was the ENOTFOUND bug.
	if err := checkSystemResolver("plug-selftest-sys", log); err != nil {
		return err
	}

	// Also prove the per-launch DNS isolation (Linux mount namespace): a child
	// must see ONLY our private resolver (nameserver dnsIP), confirming the
	// mount-ns works — under a setcap'd non-root plug too, not just as root.
	if err := checkLaunchIsolation(privResolv, dnsIP, log); err != nil {
		return err
	}
	return nil
}
