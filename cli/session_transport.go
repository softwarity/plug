package main

import (
	"net"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The session starters (startExposes, startMounts) used to take *tunnel.Transport
// and *tunnel.Exposed by their concrete types, which meant nothing between the
// dial and the teardown could run without an sshd on the other end: the one
// serve-name per name, the takeover warning, the holder offer, the re-arm after
// a reconnect and the unserve on drop had no test at all. These two interfaces
// are the slice of the tunnel package those starters actually use, and the
// adapter below is the only production implementation.

// exposedMapping is what startExposes needs of an armed -s mapping:
// *tunnel.Exposed in production, a fake in the tests.
type exposedMapping interface {
	pathVerifier
	Spec() tunnel.ExposeSpec
	AgentPort() string
	OnRearm(func())
}

// sessionTransport is the slice of *tunnel.Transport a session holds for its
// own lifetime: the control verbs, the reverse forwards, the cluster dials
// the mount helper and the path checks need, and the teardown.
type sessionTransport interface {
	envExecer
	Exec(cmd string) (string, error)
	Expose(spec tunnel.ExposeSpec) (exposedMapping, error)
	LocalAddr() string
	DialCluster(addr string) (net.Conn, error)
	DialClusterTimeout(addr string, d time.Duration) (net.Conn, error)
	Close() error
}

// tunnelSession is *tunnel.Transport as a sessionTransport. Expose has to be
// written out: the transport returns the concrete *Exposed, and a method
// returning a concrete type does not satisfy one returning an interface.
type tunnelSession struct{ *tunnel.Transport }

func (s tunnelSession) Expose(spec tunnel.ExposeSpec) (exposedMapping, error) {
	ex, err := s.Transport.Expose(spec)
	if err != nil {
		// A nil *Exposed in a non-nil interface would read as a mapping.
		return nil, err
	}
	return ex, nil
}

// dialSession is the var the session starters call, dialSessionTunnel the
// body: a seam, so the tests can put an in-memory transport under them.
// Never reassigned outside tests.
var dialSession = dialSessionTunnel

func dialSessionTunnel(cfg config) (sessionTransport, error) {
	tr, err := dialTunnel(cfg)
	if err != nil {
		return nil, err
	}
	return tunnelSession{tr}, nil
}
