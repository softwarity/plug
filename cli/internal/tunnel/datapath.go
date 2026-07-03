//go:build darwin || linux

package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/adapter"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// tunAddr is the client-side address assigned to the TUN interface. Traffic to
// the cluster subnets is routed to this interface; the address itself only has
// to be non-colliding (TEST-NET / benchmark range).
const tunAddr = "198.18.0.1"

// Run brings up a userspace TUN, routes the given cluster subnets to it, and
// relays every captured flow through the SSH transport until ctx is cancelled.
// Requires root (TUN device + routes). ready is closed once traffic can flow.
// Returns after teardown.
func Run(ctx context.Context, tr *Transport, subnets []string, logf Logf, ready chan<- struct{}) error {
	dev, err := tun.Open(tunDevName, 0)
	if err != nil {
		return fmt.Errorf("open tun (need root?): %w", err)
	}
	defer dev.Close()
	name := dev.Name()
	logf("tun %s up", name)

	h := &handler{tr: tr, logf: logf}
	st, err := core.CreateStack(&core.Config{
		LinkEndpoint:     dev,
		TransportHandler: h,
	})
	if err != nil {
		return fmt.Errorf("create netstack: %w", err)
	}
	defer st.Close()

	if err := configureInterface(name, tunAddr, subnets); err != nil {
		return fmt.Errorf("configuring routes: %w", err)
	}
	logf("tunnel up — routing %d subnet(s) into the cluster", len(subnets))
	close(ready)

	<-ctx.Done()
	logf("tearing down %s", name)
	return nil
}

// handler bridges gvisor's accepted flows to the SSH transport.
type handler struct {
	tr   *Transport
	logf Logf
}

func destination(id *stack.TransportEndpointID) string {
	ip := net.IP(id.LocalAddress.AsSlice())
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(id.LocalPort)))
}

func (h *handler) HandleTCP(conn adapter.TCPConn) {
	dst := destination(conn.ID())
	remote, err := h.tr.DialCluster(dst)
	if err != nil {
		h.logf("tcp %s: %v", dst, err)
		conn.Close()
		return
	}
	go relay(conn, remote)
}

// HandleUDP only serves DNS (:53): each query is forwarded to the cluster
// resolver over TCP through the tunnel. Other UDP is dropped (rare for the
// HTTP/gRPC services plug targets; can be added later).
func (h *handler) HandleUDP(conn adapter.UDPConn) {
	id := conn.ID()
	if id.LocalPort != 53 {
		conn.Close()
		return
	}
	go h.serveDNS(conn)
}

func (h *handler) serveDNS(conn adapter.UDPConn) {
	defer conn.Close()
	buf := make([]byte, 65535)
	for {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		resp, err := h.resolveOverTCP(buf[:n])
		if err != nil {
			h.logf("dns: %v", err)
			continue
		}
		conn.WriteTo(resp, addr)
	}
}

// resolveOverTCP sends one DNS message to the cluster resolver over a
// direct-tcpip channel (DNS-over-TCP framing) and returns the answer.
func (h *handler) resolveOverTCP(query []byte) ([]byte, error) {
	c, err := h.tr.DialCluster(clusterResolver)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	var framed [2]byte
	binary.BigEndian.PutUint16(framed[:], uint16(len(query)))
	if _, err := c.Write(append(framed[:], query...)); err != nil {
		return nil, err
	}
	var ln [2]byte
	if _, err := io.ReadFull(c, ln[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(ln[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	return resp, nil
}
