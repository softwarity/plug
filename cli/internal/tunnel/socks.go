package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

// ServeSOCKS runs a minimal SOCKS5 proxy (RFC 1928, no-auth, CONNECT) that
// tunnels every connection to the cluster through the SSH transport. In
// socks5h mode the client hands us the hostname, which we pass straight to the
// direct-tcpip channel — so sshd resolves cluster names from inside the
// cluster. No TUN, no routes, no root.
//
// It closes ready once the listener is up, then serves until ctx is cancelled.
func (t *Transport) ServeSOCKS(ctx context.Context, listenAddr string, logf Logf, ready chan<- string) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("socks listen: %w", err)
	}
	defer ln.Close()
	logf("socks5 proxy on %s", ln.Addr())
	ready <- ln.Addr().String()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go t.handleSOCKS(conn, logf)
	}
}

func (t *Transport) handleSOCKS(client net.Conn, logf Logf) {
	defer client.Close()
	dst, err := socksHandshake(client)
	if err != nil {
		return
	}
	remote, err := t.DialCluster(dst)
	if err != nil {
		logf("socks %s: %v", dst, err)
		// 0x05 = connection refused / host unreachable
		client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	// success reply (bound addr is ignored by clients for CONNECT)
	client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	relay(client, remote)
}

// socksHandshake negotiates no-auth and reads a CONNECT request, returning the
// destination "host:port" (host may be a name — passed through to the cluster).
func socksHandshake(c net.Conn) (string, error) {
	buf := make([]byte, 262)

	// greeting: VER NMETHODS METHODS...
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	if buf[0] != 0x05 {
		return "", fmt.Errorf("not socks5")
	}
	n := int(buf[1])
	if _, err := io.ReadFull(c, buf[:n]); err != nil {
		return "", err
	}
	// no authentication required
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}

	// request: VER CMD RSV ATYP DST.ADDR DST.PORT
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return "", err
	}
	if buf[1] != 0x01 { // CONNECT only
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // command not supported
		return "", fmt.Errorf("unsupported command %d", buf[1])
	}

	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return "", err
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain name
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return "", err
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return "", err
		}
		host = string(buf[:l])
	case 0x04: // IPv6
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return "", err
		}
		host = net.IP(buf[:16]).String()
	default:
		return "", fmt.Errorf("unknown address type %d", buf[3])
	}

	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(buf[:2])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}
