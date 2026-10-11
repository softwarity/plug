//go:build linux

package tun

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The netlink calls replace `ip`, so what `ip` did is what they are held to:
// the address and the route appear, a second add is refused as EEXIST, and the
// deletes take them away again. Done on lo with TEST-NET-2 addresses, which
// reach nowhere. It needs CAP_NET_ADMIN and skips without it: the unit runs are
// unprivileged, the TUN selftest exercises the same calls on a real device.
func TestNetlinkDoesWhatIPDid(t *testing.T) {
	if !hasEffCap(capNetAdmin) {
		t.Skip("needs CAP_NET_ADMIN")
	}
	addr := netip.MustParsePrefix("198.51.100.99/32")
	dst := netip.MustParsePrefix("198.51.100.128/25")

	if err := addrAdd("lo", addr); err != nil {
		t.Fatalf("addrAdd: %v", err)
	}
	t.Cleanup(func() { _ = addrDel("lo", addr) })
	if !loHas(t, addr.Addr()) {
		t.Fatalf("%s is not on lo after addrAdd", addr)
	}
	if err := addrAdd("lo", addr); !errors.Is(err, unix.EEXIST) {
		t.Errorf("a second addrAdd: got %v, want EEXIST as `ip addr add` reports it", err)
	}

	if err := routeAdd("lo", dst); err != nil {
		t.Fatalf("routeAdd: %v", err)
	}
	t.Cleanup(func() { _ = routeDel("lo", dst) })
	if !routed(t, dst) {
		t.Fatalf("no route to %s via lo after routeAdd", dst)
	}

	if err := routeDel("lo", dst); err != nil {
		t.Fatalf("routeDel: %v", err)
	}
	if routed(t, dst) {
		t.Errorf("the route to %s is still there after routeDel", dst)
	}
	if err := addrDel("lo", addr); err != nil {
		t.Fatalf("addrDel: %v", err)
	}
	if loHas(t, addr.Addr()) {
		t.Errorf("%s is still on lo after addrDel", addr)
	}
}

func loHas(t *testing.T, a netip.Addr) bool {
	t.Helper()
	ifi, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range addrs {
		if n, ok := x.(*net.IPNet); ok && n.IP.Equal(net.IP(a.AsSlice())) {
			return true
		}
	}
	return false
}

// routed reads /proc/net/route, which prints the destination and the mask as
// the 32-bit value the kernel holds, in hex: the network-order bytes read in
// host order.
func routed(t *testing.T, p netip.Prefix) bool {
	t.Helper()
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		t.Fatal(err)
	}
	hex := func(b []byte) string { return fmt.Sprintf("%08X", binary.NativeEndian.Uint32(b)) }
	a := p.Masked().Addr().As4()
	dst, mask := hex(a[:]), hex(net.CIDRMask(p.Bits(), 32))
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 8 && f[0] == "lo" && f[1] == dst && f[7] == mask {
			return true
		}
	}
	return false
}
