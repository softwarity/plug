package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// The captive-portal verdict: why a machine with hand-set DNS cannot reach the
// page it is being redirected to.
//
// A hotel, a train or an airport answers every DNS query with its own portal
// until you authenticate, and it does that from the resolver it hands out over
// DHCP. Someone who has set 1.1.1.1 by hand never asks that resolver: their
// queries go to an address the network will not route before authentication, so
// every name fails, the browser never gets redirected, and the portal cannot be
// reached to be dismissed. Nothing is broken; the machine is simply asking a
// server it is not yet allowed to talk to.
//
// plug is not the cause - it neither sets those servers nor keeps them, it
// overwrites them for the session and hands them back at teardown - but it is
// what the person is looking at when it happens, because plug is the thing that
// touches DNS. Saying so is the point: the answer is worth as much as the
// disclaimer.
//
// THE RULE IS HERE AND THE FACTS ARE GATHERED PER-OS. What counts as "the
// resolver DHCP offered" is scutil and ipconfig on macOS, resolv.conf or
// NetworkManager on Linux, a PowerShell cmdlet on Windows; the decision those
// facts feed is the same everywhere, and a rule that compiles on one OS is a
// rule nobody can test on the other two.

// publicResolvers are the well-known public ones. Not an exhaustive list and it
// does not need to be: it exists to recognise the shape of "somebody typed a
// resolver in by hand", and these are the addresses people type.
var publicResolvers = map[string]string{
	"1.1.1.1":         "Cloudflare",
	"1.0.0.1":         "Cloudflare",
	"8.8.8.8":         "Google",
	"8.8.4.4":         "Google",
	"9.9.9.9":         "Quad9",
	"149.112.112.112": "Quad9",
	"208.67.222.222":  "OpenDNS",
	"208.67.220.220":  "OpenDNS",
	"94.140.14.14":    "AdGuard",
	"76.76.2.0":       "Control D",
}

// publicResolverName returns which public resolver an address is, or "". The
// address may carry a port: the captured servers do, the configured ones do not.
func publicResolverName(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return publicResolvers[strings.TrimSpace(addr)]
}

// captiveFacts is what an OS-specific collector must find out. A field it cannot
// determine is left empty, and the verdict below stays silent rather than
// guessing: a wrong captive-portal warning sends someone to rewrite their DNS
// settings for nothing.
type captiveFacts struct {
	configured []string // the resolvers queries actually go to
	dhcp       []string // what this network offered over DHCP
	gateway    string   // the default gateway, to tell "no network" from "no DNS"
	known      bool     // whether the collector ran at all on this OS
}

// captiveVerdict is the decision, pure so it can be proven on any OS.
//
// It returns the public resolver's name when a captive portal is the likely
// explanation, and "" otherwise. Every condition has to hold:
//
//   - the resolvers in use are PUBLIC ones, i.e. hand-set. A DHCP resolver that
//     fails is a broken network, not this;
//   - the network offered a DIFFERENT resolver. Same resolver both ways means
//     nothing was overridden and there is nothing to explain;
//   - those resolvers do not answer, while the gateway does. That last pair is
//     what separates "the portal is holding DNS" from "there is no network at
//     all", and without it this fires on every flaky wifi.
func captiveVerdict(f captiveFacts, resolverAnswers, gatewayAnswers bool) string {
	name := captiveCandidate(f)
	if name == "" || resolverAnswers || !gatewayAnswers {
		return ""
	}
	return name
}

// captiveCandidate is the half that costs nothing: everything decidable from
// what was collected, with no packet sent.
//
// Split out so the probes only run when they can change the answer. They are two
// timeouts, and `plug doctor` would otherwise pay them on every single run to
// tell a machine with ordinary DHCP resolvers something it already knew.
func captiveCandidate(f captiveFacts) string {
	if !f.known || len(f.configured) == 0 || len(f.dhcp) == 0 {
		return ""
	}
	name := ""
	for _, r := range f.configured {
		n := publicResolverName(r)
		if n == "" {
			return "" // one non-public resolver and this is not the shape
		}
		if name == "" {
			name = n
		}
	}
	for _, d := range f.dhcp {
		if publicResolverName(d) == "" {
			return name // the network offered something of its own: overridden
		}
	}
	return "" // public resolvers both ways: nothing was overridden
}

// resolverAnswers asks one resolver for a name, briefly. It is the probe, not the
// rule, and it is Go rather than a per-OS command on purpose: dialling a UDP
// socket is the same everywhere.
//
// The name is one that exists and is not the portal's: a captive network answers
// EVERYTHING with its own address, so "did it answer" is the question, never
// "what did it say".
func resolverAnswers(addr string, timeout time.Duration) bool {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "53")
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := r.LookupHost(ctx, "one.one.one.one.")
	return err == nil
}

// gatewayAnswers reports whether the default gateway accepts a connection, which
// is how "the portal is holding DNS" is told apart from "there is no network".
//
// A refused connection COUNTS as an answer: something is there and said no,
// which is all this needs to know. Only a timeout means nothing is listening at
// all - and a captive gateway almost always serves the portal on 80 anyway.
func gatewayAnswers(gw string, timeout time.Duration) bool {
	if gw == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(gw, "80"), timeout)
	if err == nil {
		_ = c.Close()
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	return true // refused, reset: somebody is home
}
