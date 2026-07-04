//go:build darwin || linux

package tunnel

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/adapter"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// tunAddr is the client-side address assigned to the TUN interface. Traffic to
// the cluster subnets is routed to this interface; the address itself only has
// to be non-colliding (TEST-NET / benchmark range).
const tunAddr = "198.18.0.1"

// Run brings up a userspace TUN, routes the given cluster subnets to it, sets
// up split-horizon DNS, and relays every captured flow through the SSH
// transport until ctx is cancelled. Requires root (TUN device + routes + DNS
// redirect). ready is closed once traffic can flow. Returns after teardown.
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

	// Split-horizon DNS: a local resolver + a redirect of the system's :53 to
	// it. Restored on teardown (defer runs before the TUN closes).
	restoreDNS, err := setupDNS(ctx, tr, logf)
	if err != nil {
		logf("DNS not redirected (%v) — names may not resolve, IPs still work", err)
	} else {
		defer restoreDNS()
	}

	logf("tunnel up — routing %d subnet(s) + DNS into the cluster", len(subnets))
	close(ready)

	<-ctx.Done()
	logf("tearing down %s", name)
	return nil
}

// handler bridges gvisor's accepted TCP flows to the SSH transport. DNS is
// handled out of band by the split resolver, so UDP is dropped here.
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

func (h *handler) HandleUDP(conn adapter.UDPConn) {
	conn.Close() // DNS goes through the split resolver, not the TUN
}
