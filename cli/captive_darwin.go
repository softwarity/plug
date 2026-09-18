package main

import (
	"net"
	"os/exec"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
)

// captiveCollect gathers, on macOS, the facts captiveVerdict decides from.
//
// Three commands, and none of them is a guess: the default route names both the
// gateway and the interface it leaves by, `ipconfig getpacket` reads that
// interface's actual DHCP lease, and the resolvers come from plug itself - the
// published upstreams during a session, the primary service's configuration
// otherwise.
func captiveCollect() captiveFacts {
	f := captiveFacts{}
	gw, iface := defaultRoute()
	f.gateway = gw
	if iface == "" {
		return f // no default route: there is nothing to diagnose, and no portal either
	}
	f.dhcp = dhcpResolvers(iface)
	// During a session the system resolver is plug, so what the queries really
	// leave on is what the datapath captured. Without one, the configuration is
	// the answer - and that is the case this exists for.
	if f.configured = tun.CurrentUpstreams(); len(f.configured) == 0 {
		f.configured = tun.SystemResolvers()
	}
	f.known = true
	return f
}

// defaultRoute returns the default gateway and the interface it leaves by.
func defaultRoute() (gateway, iface string) {
	out, err := exec.Command(tun.HelperPath("route"), "-n", "get", "default").Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "gateway":
			gateway = strings.TrimSpace(v)
		case "interface":
			iface = strings.TrimSpace(v)
		}
	}
	return gateway, iface
}

// dhcpResolvers reads the resolvers THIS NETWORK offered, from the interface's
// DHCP lease rather than from anything the user configured. That is the whole
// point of the comparison: the lease is what the network wanted you to use, and
// on a captive network it is the only resolver that answers.
//
// scutil would not do here. It reports the resolvers in EFFECT, which are the
// hand-set ones - the very thing being questioned.
func dhcpResolvers(iface string) []string {
	out, err := exec.Command(tun.HelperPath("ipconfig"), "getpacket", iface).Output()
	if err != nil {
		return nil
	}
	// domain_name_server (option 6) prints as:
	//   domain_name_server (ip_mult): {192.168.10.1, 192.168.10.2}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "domain_name_server") {
			continue
		}
		_, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(strings.TrimSuffix(v, "}"), "{")
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// captiveRemedy names both directions, and the second is not optional: clearing
// the resolvers removes a setting somebody chose on purpose - for privacy, or to
// get around an ISP - and a remedy that destroys a choice without handing back
// the way to restore it is worse than no remedy. The current values are right
// there, so they go in the line.
func captiveRemedy(f captiveFacts) string {
	svc := networkServiceFor(f)
	back := make([]string, 0, len(f.configured))
	for _, r := range f.configured {
		if h, _, err := net.SplitHostPort(r); err == nil {
			r = h
		}
		back = append(back, r)
	}
	return "let this network's own resolver answer while you sign in:\n" +
		"      sudo networksetup -setdnsservers \"" + svc + "\" empty\n" +
		"      then put yours back once you are through:\n" +
		"      sudo networksetup -setdnsservers \"" + svc + "\" " + strings.Join(back, " ")
}

// networkServiceFor turns the interface the default route leaves by (en0) into
// the name networksetup wants ("Wi-Fi"), which is not the same string and is not
// always in English. Falls back to Wi-Fi, which is what it is on a train.
func networkServiceFor(f captiveFacts) string {
	_, iface := defaultRoute()
	out, err := exec.Command(tun.HelperPath("networksetup"), "-listnetworkserviceorder").Output()
	if err != nil || iface == "" {
		return "Wi-Fi"
	}
	// Entries print as:
	//   (1) Wi-Fi
	//   (Hardware Port: Wi-Fi, Device: en0)
	name := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if n, ok := strings.CutPrefix(line, "("); ok {
			if _, rest, found := strings.Cut(n, ") "); found {
				name = rest
			}
		}
		if strings.Contains(line, "Device: "+iface+")") && name != "" {
			return name
		}
	}
	return "Wi-Fi"
}
