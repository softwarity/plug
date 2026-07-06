// Package tun is plug's root-mode data path: ONE cross-platform userspace TUN
// that captures the child's cluster traffic at the IP layer and forwards it
// through the SSH tunnel. It replaces every fd-level trick (the LD_PRELOAD
// hook's dup2, the seccomp supervisor's ADDFD — both swap the socket fd and
// strand event-loop epoll registrations, which is why gRPC/Netty/grpcio break).
//
// The pipeline, identical on Linux/macOS/Windows:
//
//   - wireguard-go/tun opens the device (/dev/net/tun, utun, WinTUN);
//   - a tiny DNS server answers the child's lookups, minting a fake IP
//     (240.0.0.0/4) per single-label cluster name (dotted → real upstream,
//     localhost → loopback);
//   - the OS routes 240/4 to the TUN, so the child's connect() to a fake IP
//     surfaces as an IP packet we read;
//   - a gVisor userspace netstack terminates that TCP flow and hands us a
//     net.Conn; we map the fake IP back to the name and splice it to the SSH
//     tunnel by name.
//
// The child's socket is NEVER touched, so this covers EVERY runtime uniformly.
// Creating the TUN + setting routes needs root (or the plug helper); opt-in via
// `plug --tun`.
package tun

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

// Dialer is the cluster transport (satisfied by *tunnel.Transport).
type Dialer interface {
	DialCluster(addr string) (net.Conn, error)
}

const (
	mtu      = 1500        // TUN MTU; gVisor segments egress to this
	headroom = 16          // scratch bytes wireguard-go needs before each packet
	nicID    = 1           // the single netstack NIC
	fakeCIDR = "240.0.0.0/4" // class-E space we route into the TUN
	dnsAddr  = "127.0.0.1"   // where the child's resolver is pointed (port 53)
)

// NsShimVerb is the hidden re-exec subcommand plug uses to enter a child's mount
// namespace on Linux and bind-mount its private resolv.conf (see NsShimMain). Not
// user-facing — dispatched at the very top of main().
const NsShimVerb = "__plug-ns"

// logfn is an optional progress sink.
type logfn func(string, ...any)

func (l logfn) f(format string, a ...any) {
	if l != nil {
		l(format, a...)
	}
}

// Run sets up the TUN data path and runs cmdArgs under it, forwarding cluster
// connections through tr. Returns the child's exit code.
func Run(tr Dialer, cmdArgs []string, logf func(string, ...any)) (int, error) {
	log := logfn(logf)
	if err := checkPriv(); err != nil {
		return 1, err
	}

	dev, err := wgtun.CreateTUN(defaultTUNName, mtu)
	if err != nil {
		return 1, fmt.Errorf("create TUN (need root/helper): %w", err)
	}
	defer dev.Close()
	ifname, _ := dev.Name()

	// Networking + DNS handoff (privileged, per-OS). Returns the child's former
	// upstream nameservers (captured so our own dotted-name lookups don't loop) and,
	// on Linux, the path to a PRIVATE resolv.conf we bind-mount into the child's
	// mount namespace — so pointing the resolver at us never touches the global
	// /etc/resolv.conf and two `plug` runs never collide.
	upstreams, privResolv, cleanup, err := configure(ifname, dnsAddr, log)
	if err != nil {
		return 1, fmt.Errorf("configure %s: %w", ifname, err)
	}
	defer cleanup()

	tab := newFaketab()

	// Fake DNS on loopback :53 — the child reaches it directly (not via the TUN).
	dnsConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(dnsAddr), Port: 53})
	if err != nil {
		return 1, fmt.Errorf("dns %s:53: %w", dnsAddr, err)
	}
	defer dnsConn.Close()
	go serveDNS(dnsConn, tab, upstreamResolver(upstreams), log)

	// Userspace netstack fed by the TUN.
	st, ep := buildStack(tab, tr, log)
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &bridge{dev: dev, ep: ep}
	go br.toStack()      // TUN packets  → netstack
	go br.fromStack(ctx) // netstack replies → TUN

	log.f("root mode (TUN %s): DNS %s:53, %s → netstack → tunnel (covers every runtime)", ifname, dnsAddr, fakeCIDR)
	return runChild(cmdArgs, privResolv)
}

// bridge pumps IP packets between the wireguard-go device and the gVisor
// channel endpoint. wireguard-go strips/prepends any platform header itself and
// hands us pure IP packets at `headroom`, in batches of up to BatchSize().
type bridge struct {
	dev wgtun.Device
	ep  *channel.Endpoint
}

func (b *bridge) toStack() {
	batch := b.dev.BatchSize()
	bufs := make([][]byte, batch)
	sizes := make([]int, batch)
	for i := range bufs {
		bufs[i] = make([]byte, headroom+65535) // 65535: a GRO-coalesced read may exceed MTU
	}
	for {
		n, err := b.dev.Read(bufs, sizes, headroom)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			if sizes[i] == 0 {
				continue
			}
			pkt := bufs[i][headroom : headroom+sizes[i]]
			var proto tcpip.NetworkProtocolNumber
			switch pkt[0] >> 4 {
			case 4:
				proto = header.IPv4ProtocolNumber
			case 6:
				proto = header.IPv6ProtocolNumber
			default:
				continue
			}
			pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
			b.ep.InjectInbound(proto, pkb)
			pkb.DecRef()
		}
	}
}

func (b *bridge) fromStack(ctx context.Context) {
	for {
		pkb := b.ep.ReadContext(ctx)
		if pkb == nil {
			return // ctx cancelled
		}
		v := pkb.ToView()
		if v != nil {
			data := v.AsSlice()
			buf := make([]byte, headroom+len(data))
			copy(buf[headroom:], data)
			b.dev.Write([][]byte{buf}, headroom)
		}
		pkb.DecRef()
	}
}

// run executes a privileged setup command (ip/route/ifconfig/netsh), surfacing
// its output on failure. Shared by the per-OS configure() implementations.
func run(bin string, args ...string) error {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ipStr renders a packed IPv4 fake address for logs.
func ipStr(ip uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], ip)
	return net.IP(b[:]).String()
}
