package tun

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// buildStack wires a userspace TCP/IP stack onto a channel endpoint. The NIC is
// promiscuous + spoofing with a catch-all v4 route, so the stack accepts SYNs to
// ANY fake destination and may reply from it. Every accepted TCP flow is handed
// to handleTCP, which splices it to the SSH tunnel by name.
func buildStack(tab *faketab, tr Dialer, log logfn) (*stack.Stack, *channel.Endpoint) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	ep := channel.New(512, mtu, "")
	s.CreateNIC(nicID, ep)
	s.SetPromiscuousMode(nicID, true)
	s.SetSpoofing(nicID, true)
	// All fakes are IPv4 (the resolver hands out 240/4 and NODATAs AAAA), so a
	// single v4 catch-all route suffices; stray v6 packets are simply dropped.
	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: nicID})

	fwd := tcp.NewForwarder(s, 0, 2048, func(r *tcp.ForwarderRequest) {
		handleTCP(r, tab, tr, log)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
	return s, ep
}

// handleTCP completes the netstack handshake for one intercepted flow, maps its
// fake destination back to the cluster name, dials the tunnel, and relays.
func handleTCP(r *tcp.ForwarderRequest, tab *faketab, tr Dialer, log logfn) {
	id := r.ID()
	fake := addrToU32(id.LocalAddress) // LocalAddress/Port = the original destination
	name, ok := tab.lookup(fake)
	if !ok {
		r.Complete(true) // RST — we never minted this fake
		return
	}

	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	local := gonet.NewTCPConn(&wq, ep)

	target := net.JoinHostPort(name, strconv.Itoa(int(id.LocalPort)))
	up, err := tr.DialCluster(target)
	if err != nil {
		log.f("tun: cluster %s: %v", target, err)
		local.Close()
		return
	}
	log.f("tun: %s:%d → %s (by name)", ipStr(fake), id.LocalPort, target)
	go relay(local, up)
}

// closeWriter is the half-close capability shared by gonet.TCPConn, *net.TCPConn
// and the SSH channel conn — used to propagate EOF in one direction.
type closeWriter interface{ CloseWrite() error }

// relay splices two connections, half-closing each side on EOF and tearing both
// down once both directions finish.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// addrToU32 packs a gVisor IPv4 address into the fake-table key space.
func addrToU32(a tcpip.Address) uint32 {
	if a.Len() != 4 {
		return 0
	}
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}
