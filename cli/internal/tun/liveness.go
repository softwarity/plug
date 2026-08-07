package tun

import (
	"context"
	"net"
	"time"
)

// DatapathResponsive asks the running datapath's own in-stack resolver whether
// it is still answering — a process being alive says nothing about the netstack
// inside it still working.
//
// It asks for "localhost", which answerDNS serves from its own code with no
// cluster, no agent and no upstream involved. Any other name would drag in the
// tunnel or a forwarded lookup, and a slow ANSWER would be indistinguishable
// from no answer at all — which is precisely the distinction being made here.
//
// NXDOMAIN would count as answering too, but localhost cannot produce one: a
// reply means the stack is live, a timeout means it is not.
func DatapathResponsive(timeout time.Duration) bool {
	_, dnsIP, _ := instanceNet(0)
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(dnsIP, "53"))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := res.LookupHost(ctx, "localhost")
	return err == nil && len(ips) > 0
}
