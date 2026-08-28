// Package tun is plug's root-mode data path: ONE cross-platform userspace TUN
// that captures the child's cluster traffic at the IP layer and forwards it
// through the SSH tunnel. It replaces every fd-level trick (the LD_PRELOAD
// hook's dup2, the seccomp supervisor's ADDFD — both swap the socket fd and
// strand event-loop epoll registrations, which is why gRPC/Netty/grpcio break).
//
// The pipeline, identical on Linux/macOS/Windows:
//
//   - wireguard-go/tun opens the device (/dev/net/tun, utun, WinTUN);
//   - a gVisor userspace netstack, fed by the device, answers the child's DNS
//     in-stack on 198.18.<N>.53:53 — minting a fake IP in 198.18.<N>.0/24 per
//     single-label cluster name (dotted → real upstream, localhost → loopback);
//   - the OS routes that /24 to the TUN, so the child's connect() to a fake IP
//     surfaces as an IP packet we read;
//   - the netstack terminates that TCP flow and hands us a net.Conn; we map the
//     fake IP back to the name and splice it to the SSH tunnel by name.
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
	"sync"

	wgtun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// Dialer is the cluster transport (satisfied by *tunnel.Transport).
type Dialer interface {
	DialCluster(addr string) (net.Conn, error)
}

const (
	mtu      = 1500 // TUN MTU; gVisor segments egress to this
	headroom = 16   // scratch bytes wireguard-go needs before each packet
	nicID    = 1    // the single netstack NIC
)

// instanceNet derives instance N's routed subnet and reserved DNS address. Each
// instance owns 198.18.<N>.0/24 (routed into its own TUN) and answers DNS on
// 198.18.<N>.53 — so concurrent instances never overlap. N is allocated by
// claiming the first free TUN device (see startDatapathDF).
func instanceNet(n int) (cidr, dnsIP string, base uint32) {
	base = uint32(fakeBase) | uint32(n)<<8 // 198.18.<n>.0
	cidr = ipStr(base) + "/24"
	dnsIP = ipStr(base | dnsHost)
	return
}

// NsShimVerb is the hidden re-exec subcommand plug uses to enter a child's mount
// namespace on Linux and bind-mount its private resolv.conf (see NsShimMain). Not
// user-facing — dispatched at the very top of main().
const NsShimVerb = "__plug-ns"

// DaemonVerb is the hidden re-exec subcommand that runs the persistent macOS
// datapath daemon for one cluster (see daemonMain). Dispatched at the top of main().
const DaemonVerb = "__plug-daemon"

// logfn is an optional progress sink.
type logfn func(string, ...any)

func (l logfn) f(format string, a ...any) {
	if l != nil {
		l(format, a...)
	}
}

// Datapath is a live TUN data path: the utun, its routes + DNS repoint, the
// gVisor netstack, and the bridge pumping IP packets between them. It stays up
// until Stop — so a daemon can hold it across many child processes. privResolv is
// the child's private resolv.conf path on Linux ("" elsewhere).
type Datapath struct {
	Ifname     string
	DNSIP      string
	privResolv string
	stop       func()
	done       chan struct{}
	once       sync.Once
}

// StartDatapath brings up the TUN, configures routes + the system/child DNS,
// mounts the netstack and starts the bridge — WITHOUT running a child. tr is the
// already-dialed cluster transport. The caller must Stop() it to tear everything
// down (routes + DNS restored). This is the piece a daemon holds; Run wraps it.
func StartDatapath(tr Dialer, logf func(string, ...any)) (*Datapath, error) {
	return startDatapathDF(constDial(tr), func() []Dialer { return []Dialer{tr} }, logf)
}

// startDatapathDF is StartDatapath's core, parameterized by the dialFunc that
// routes each intercepted flow to a transport: constDial for a single cluster,
// multiDial for the global multicluster daemon. It brings up the TUN + routes +
// DNS + netstack + bridge, identically in both cases.
func startDatapathDF(df dialFunc, dialers func() []Dialer, logf func(string, ...any)) (*Datapath, error) {
	log := logfn(logf)
	if err := checkPriv(); err != nil {
		return nil, err
	}

	// Allocate the instance slot N by claiming the first free TUN device: on Linux
	// each simultaneous launch (one per cluster) gets its own plug<N> — the kernel
	// arbitrates the name, so a taken device ("busy") just means try the next slot.
	// The slot then derives the instance's OWN 198.18.<N>.0/24 + DNS, so concurrent
	// clusters never overlap. macOS/Windows hold one datapath per machine
	// (daemon / SYSTEM service): a single slot.
	var dev wgtun.Device
	var err error
	n := 0
	for ; n < maxInstances; n++ {
		if dev, err = wgtun.CreateTUN(tunNameFor(n), mtu); err == nil {
			break
		}
		if !strings.Contains(err.Error(), "busy") {
			break // permission or driver problem — more names won't help
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create TUN (need root/helper): %w", err)
	}
	cidr, dnsIP, base := instanceNet(n)
	ifname, _ := dev.Name()

	// Networking + DNS handoff (privileged, per-OS). Routes cidr into the TUN and
	// points the system resolver at dnsIP (198.18.<N>.53) — by the native channel
	// of each OS: a bind-mounted PRIVATE resolv.conf on Linux (scoped to the child
	// via its mount namespace), the SystemConfiguration dynamic store on macOS,
	// the adapter DNS on Windows. Returns the child's former upstream nameservers
	// (captured so our own dotted-name lookups don't loop) and, on Linux, the path
	// to that private resolv.conf.
	up := newUpstream(nil)
	upstreams, privResolv, cleanup, err := configure(dev, n, ifname, cidr, dnsIP, up, log)
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure %s: %w", ifname, err)
	}

	tab := newFaketab(base)

	// Userspace netstack fed by the TUN. DNS is answered INSIDE the stack on
	// dnsIP:53 (a UDP forwarder), reached by the child through the TUN — no
	// loopback socket, so macOS's getaddrinfo (which ignores /etc/resolv.conf)
	// resolves cluster names via the system resolver we just repointed at dnsIP.
	// Pre-mint existence check: a bare name is only minted if it exists in a
	// connected cluster (asked through the agent, cached) — an absent name gets
	// an honest NXDOMAIN. See newNameChecker for the fallbacks.
	check := newNameChecker(dialers, log)
	// No captured upstream means every DOTTED name — everything that is not a
	// cluster service — goes to a public resolver we picked. That is a real
	// change to where this machine's DNS traffic goes, and it happens silently
	// on Windows today because configure() there returns none. Say it: a user on
	// a corporate network needs to know their internal names will not resolve
	// and their lookups are leaving.
	if len(upstreams) == 0 {
		log("tun: no system resolver was captured — dotted names will be forwarded to a PUBLIC resolver.\n" +
			"      Internal names may stop resolving, and those lookups leave your network.")
	}
	// Seed it with what configure just captured. From here the per-OS watcher
	// inside configure keeps it current: the servers are not a startup fact, and
	// a VPN coming up mid-session replaces them.
	up.set(upstreams)
	st, ep := buildStack(tab, df, up, check, log)

	ctx, cancel := context.WithCancel(context.Background())
	br := &bridge{dev: dev, ep: ep}
	go br.toStack()      // TUN packets  → netstack
	go br.fromStack(ctx) // netstack replies → TUN

	log.f("root mode (TUN %s): DNS %s:53 in-netstack, %s → tunnel (covers every runtime)", ifname, dnsIP, cidr)

	dp := &Datapath{Ifname: ifname, DNSIP: dnsIP, privResolv: privResolv, done: make(chan struct{})}
	dp.stop = func() {
		cancel()         // stop the bridge's fromStack loop
		st.Close()       // tear down the netstack
		cleanup()        // restore routes + system/child DNS
		dev.Close()      // remove the utun
		ClearUpstreams() // nothing is forwarding any more — say nothing rather than the last thing that was true
	}
	return dp, nil
}

// Wait blocks until Stop is called (used by the daemon to hold the datapath).
func (d *Datapath) Wait() { <-d.done }

// Stop tears the datapath down exactly once and unblocks Wait. Safe to call from
// a signal handler, a defer and the reaper concurrently.
func (d *Datapath) Stop() {
	d.once.Do(func() {
		d.stop()
		close(d.done)
	})
}

// Run sets up the TUN data path and runs cmdArgs under it, forwarding cluster
// connections through tr. It is the standalone path — still used on Linux and as
// the non-daemon fallback. Returns the child's exit code.
func Run(tr Dialer, cmdArgs []string, logf func(string, ...any)) (int, error) {
	dp, err := StartDatapath(tr, logf)
	if err != nil {
		return 1, err
	}
	defer dp.Stop()
	return runChild(cmdArgs, dp.privResolv)
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
// withPrivCaps (Linux) passes plug's capabilities down to the helper, which
// otherwise runs with none — file capabilities don't cross an exec.
func run(bin string, args ...string) error {
	// Resolved against root-owned system directories, never the caller's $PATH:
	// withPrivCaps below hands this process plug's capabilities, so a bare name
	// is a fake `ip` away from CAP_SYS_ADMIN. See helperPath.
	path, ok := helperPath(bin)
	if !ok {
		return fmt.Errorf("%s: not found in %s. plug looks for its privileged helpers "+
			"there and not on $PATH, because it hands them its own privilege", bin, helperDirsList())
	}
	cmd := exec.Command(path, args...)
	withPrivCaps(cmd)
	out, err := cmd.CombinedOutput()
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
