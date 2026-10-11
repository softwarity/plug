//go:build linux

package tun

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// The TUN's link, address and route are set by talking rtnetlink to the kernel
// directly, the same three messages `ip link set up`, `ip addr add` and
// `ip route add` send. plug used to exec `ip` for them, which made iproute2 a
// requirement of every Linux machine and container plug runs in, and put a
// helper looked up on disk between plug and its privilege (see helperPath). In
// process, the capability that does the work is the one plug already holds.

// linkUp brings ifname up (`ip link set <ifname> up`).
func linkUp(ifname string) error {
	idx, err := ifIndex(ifname)
	if err != nil {
		return err
	}
	msg := make([]byte, unix.SizeofIfInfomsg)
	msg[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(msg[4:8], uint32(idx))
	binary.NativeEndian.PutUint32(msg[8:12], unix.IFF_UP)  // flags
	binary.NativeEndian.PutUint32(msg[12:16], unix.IFF_UP) // change: only this bit
	if err := rtnetlink(unix.RTM_NEWLINK, 0, msg); err != nil {
		return fmt.Errorf("set %s up: %w", ifname, err)
	}
	return nil
}

// addrAdd and addrDel put local (an address with its prefix length, as
// 10.99.99.1/24) on ifname and take it off (`ip addr add|del <local> dev
// <ifname>`). As with `ip`, the kernel adds the connected route of the prefix.
func addrAdd(ifname string, local netip.Prefix) error {
	return addrChange(unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_EXCL, "add", ifname, local)
}

func addrDel(ifname string, local netip.Prefix) error {
	return addrChange(unix.RTM_DELADDR, 0, "del", ifname, local)
}

func addrChange(msgType, flags uint16, verb, ifname string, local netip.Prefix) error {
	idx, err := ifIndex(ifname)
	if err != nil {
		return err
	}
	if !local.Addr().Is4() {
		return fmt.Errorf("%s %s on %s: not an IPv4 address", verb, local, ifname)
	}
	msg := make([]byte, unix.SizeofIfAddrmsg)
	msg[0] = unix.AF_INET
	msg[1] = byte(local.Bits())
	binary.NativeEndian.PutUint32(msg[4:8], uint32(idx))
	a := local.Addr().As4()
	msg = append(msg, rtattr(unix.IFA_LOCAL, a[:])...)
	msg = append(msg, rtattr(unix.IFA_ADDRESS, a[:])...)
	if err := rtnetlink(msgType, flags, msg); err != nil {
		return fmt.Errorf("%s %s on %s: %w", verb, local, ifname, err)
	}
	return nil
}

// routeAdd and routeDel route dst into ifname and stop doing so (`ip route
// add|del <dst> dev <ifname>`): the main table, link scope, no gateway.
func routeAdd(ifname string, dst netip.Prefix) error {
	return routeChange(unix.RTM_NEWROUTE, unix.NLM_F_CREATE|unix.NLM_F_EXCL, "add", ifname, dst)
}

func routeDel(ifname string, dst netip.Prefix) error {
	return routeChange(unix.RTM_DELROUTE, 0, "del", ifname, dst)
}

func routeChange(msgType, flags uint16, verb, ifname string, dst netip.Prefix) error {
	idx, err := ifIndex(ifname)
	if err != nil {
		return err
	}
	dst = dst.Masked()
	if !dst.Addr().Is4() {
		return fmt.Errorf("route %s %s via %s: not an IPv4 prefix", verb, dst, ifname)
	}
	msg := make([]byte, unix.SizeofRtMsg)
	msg[0] = unix.AF_INET
	msg[1] = byte(dst.Bits())
	msg[4] = unix.RT_TABLE_MAIN
	if msgType == unix.RTM_NEWROUTE {
		msg[5] = unix.RTPROT_BOOT
		msg[6] = unix.RT_SCOPE_LINK
		msg[7] = unix.RTN_UNICAST
	} else {
		// What `ip route del` sends: any protocol, any type, the scope left open.
		msg[6] = unix.RT_SCOPE_NOWHERE
	}
	a := dst.Addr().As4()
	oif := make([]byte, 4)
	binary.NativeEndian.PutUint32(oif, uint32(idx))
	msg = append(msg, rtattr(unix.RTA_DST, a[:])...)
	msg = append(msg, rtattr(unix.RTA_OIF, oif)...)
	if err := rtnetlink(msgType, flags, msg); err != nil {
		return fmt.Errorf("route %s %s via %s: %w", verb, dst, ifname, err)
	}
	return nil
}

func ifIndex(ifname string) (int, error) {
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return 0, fmt.Errorf("interface %s: %w", ifname, err)
	}
	return ifi.Index, nil
}

// rtattr encodes one netlink attribute, padded to the 4-byte alignment the
// kernel reads them at.
func rtattr(typ uint16, data []byte) []byte {
	n := unix.SizeofRtAttr + len(data)
	b := make([]byte, (n+unix.RTA_ALIGNTO-1)&^(unix.RTA_ALIGNTO-1))
	binary.NativeEndian.PutUint16(b[0:2], uint16(n))
	binary.NativeEndian.PutUint16(b[2:4], typ)
	copy(b[unix.SizeofRtAttr:], data)
	return b
}

// rtnetlink sends one request to the kernel and returns its answer: nil for an
// acknowledgement, the errno it refused with otherwise (EEXIST, EPERM...).
func rtnetlink(msgType, flags uint16, body []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("netlink socket: %w", err)
	}
	defer unix.Close(fd)
	kernel := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("netlink bind: %w", err)
	}

	const seq = 1
	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(body))
	msg = append(msg, body...)
	binary.NativeEndian.PutUint32(msg[0:4], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:6], msgType)
	binary.NativeEndian.PutUint16(msg[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK|flags)
	binary.NativeEndian.PutUint32(msg[8:12], seq)
	if err := unix.Sendto(fd, msg, 0, kernel); err != nil {
		return fmt.Errorf("netlink send: %w", err)
	}

	buf := make([]byte, 1<<16)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return fmt.Errorf("netlink receive: %w", err)
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return fmt.Errorf("netlink answer: %w", err)
		}
		for _, m := range msgs {
			if m.Header.Seq != seq || m.Header.Type != unix.NLMSG_ERROR {
				continue
			}
			if len(m.Data) < 4 {
				return fmt.Errorf("netlink answer: truncated")
			}
			if errno := int32(binary.NativeEndian.Uint32(m.Data[0:4])); errno != 0 {
				return unix.Errno(-errno)
			}
			return nil
		}
	}
}
