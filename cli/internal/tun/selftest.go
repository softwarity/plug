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

	// The fake-VPN probe is opt-in: it fabricates a network address (and, on
	// Windows, an adapter) rather than only reading the machine, so a bare
	// `plug selftest` does not do it. scripts/selftest.sh turns it on, which is
	// what CI runs — the probe is covered on all three OSes there.
	vpnProbe := os.Getenv("PLUG_SELFTEST_VPN") == "1"
	if vpnProbe {
		// The production poll is human-scale, and the probe waits on it twice.
		// Shortened BEFORE configure, which is what starts the watcher.
		upstreamPoll = 500 * time.Millisecond
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

	up := newUpstream(nil)
	upstreams, privResolv, cleanup, err := configure(dev, 0, ifname, cidr, dnsIP, up, log)
	if err != nil {
		return fmt.Errorf("configure %s: %w", ifname, err)
	}
	// Capturing the machine's own nameservers is what keeps a dotted name going
	// where this machine's DNS already went. With none, plug forwards to a public
	// resolver instead: internal names stop resolving and those lookups leave the
	// network. Only the live adapter table can show it works, so assert it here —
	// this is the one test that runs elevated, against the real OS, on all three.
	//
	// Not fatal: a CI runner or a container can genuinely have no nameserver on a
	// non-loopback interface, and failing the selftest for the machine's own
	// network shape would be a flake. Loud is enough to notice a regression.
	// What production does right after configure (tun.go): without it the stub
	// would relay to the public fallback here, and the probe below would be
	// asserting against a datapath no user ever runs.
	up.set(upstreams)
	if len(upstreams) == 0 {
		log.f("selftest: WARNING no system nameserver captured — dotted names would go to a public resolver")
	} else {
		log.f("selftest: captured %d system nameserver(s), first %s", len(upstreams), upstreams[0])
		for _, u := range upstreams {
			if inFakeRange(u) {
				return fmt.Errorf("selftest: captured %s as an upstream — that is plug's own range, "+
					"forwarding there is an unbounded loop", u)
			}
		}
	}
	// The DNS repoint is not the only piece of state that outlives this process —
	// the VPN probe below adds an address, and an adapter on Windows — so a Ctrl-C
	// mid-test MUST still undo all of it. Undo in reverse order (the probe's rig
	// comes off before the repoint it was driving), exactly once, from either the
	// normal return path or a signal.
	var undoMu sync.Mutex
	// ClearUpstreams too: the self-test publishes where `plug doctor` reads, and
	// leaving that behind would have doctor report a datapath that is long gone.
	undo := []func(){cleanup, ClearUpstreams}
	addUndo := func(f func()) {
		undoMu.Lock()
		defer undoMu.Unlock()
		undo = append(undo, f)
	}
	var once sync.Once
	doCleanup := func() {
		once.Do(func() {
			undoMu.Lock()
			defer undoMu.Unlock()
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		})
	}
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

	st, ep := buildStack(tab, constDial(loopbackDialer{addr: echo.Addr().String()}), up, nil, log)
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &bridge{dev: dev, ep: ep, log: log}
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

	// Last, because it moves the machine's resolvers under the running datapath:
	// everything above asserts against the state configure() set up, and this is
	// the one check that deliberately changes it.
	if vpnProbe {
		if err := probeVPNFollowing(up, dnsIP, upstreams, addUndo, log); err != nil {
			return err
		}
	}
	return nil
}
