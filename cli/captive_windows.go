//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/softwarity/plug/cli/internal/tun"
)

// captiveCollect gathers the three facts the shared rule needs, the Windows way.
//
// The comparison the verdict rests on - what DHCP offered against what queries
// really go to - is exactly what Windows keeps per interface under
// Tcpip\Parameters\Interfaces\<GUID>: DhcpNameServer is the lease, NameServer
// is what somebody typed in (empty when nobody did). The interface is the one
// carrying the default gateway, read from the adapter table the datapath
// already uses. No PowerShell: that is 1.5s of start-up doctor would pay on
// every run to learn what the registry says at once.
func captiveCollect() captiveFacts {
	f := captiveFacts{}
	gw, guid := defaultRouteAdapter()
	f.gateway = gw
	if guid == "" {
		return f // no default route: nothing to diagnose, and no portal either
	}
	if f.configured = tun.CurrentUpstreams(); len(f.configured) == 0 {
		f.configured = interfaceResolvers(guid, "NameServer")
	}
	f.dhcp = interfaceResolvers(guid, "DhcpNameServer")
	f.known = true
	return f
}

// defaultRouteAdapter returns the default gateway and the GUID of the adapter it
// leaves by. The adapter table is asked for gateways explicitly; the default
// flags leave them out.
func defaultRouteAdapter() (gateway, guid string) {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagIncludeGateways)
	if err != nil {
		return "", ""
	}
	best := uint32(0)
	for _, a := range adapters {
		if a.OperStatus != winipcfg.IfOperStatusUp || a.FirstGatewayAddress == nil {
			continue
		}
		ip := a.FirstGatewayAddress.Address.IP()
		if ip == nil {
			continue
		}
		if guid == "" || a.Ipv4Metric < best {
			gateway, guid, best = ip.String(), a.AdapterName(), a.Ipv4Metric
		}
	}
	return gateway, guid
}

// interfaceResolvers reads one of the per-interface resolver values, as
// Windows stores them: a single string, servers separated by spaces or commas.
func interfaceResolvers(guid, value string) []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\`+guid, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	raw, _, _ := k.GetStringValue(value)
	return splitResolverList(raw)
}

// captiveRemedy names the exact PowerShell that lets the network's own resolver
// answer, and the one that puts the person's servers back. Both lines, because
// the first alone is how a machine ends up on the wrong resolver for good - the
// laptop this was written on spent a day that way.
func captiveRemedy(f captiveFacts) string {
	_, guid := defaultRouteAdapter()
	alias := adapterAlias(guid)
	back := make([]string, 0, len(f.configured))
	for _, r := range f.configured {
		if i := strings.LastIndex(r, ":"); i > 0 && !strings.Contains(r, "]") && strings.Count(r, ":") == 1 {
			r = r[:i]
		}
		back = append(back, `"`+r+`"`)
	}
	return "let this network's own resolver answer while you sign in (an elevated PowerShell):\n" +
		"      Set-DnsClientServerAddress -InterfaceAlias \"" + alias + "\" -ResetServerAddresses\n" +
		"      then put yours back once you are through:\n" +
		"      Set-DnsClientServerAddress -InterfaceAlias \"" + alias + "\" -ServerAddresses " + strings.Join(back, ",")
}

// adapterAlias turns the adapter GUID into the name the cmdlets want ("Wi-Fi").
func adapterAlias(guid string) string {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_INET, winipcfg.GAAFlagDefault)
	if err == nil {
		for _, a := range adapters {
			if a.AdapterName() == guid {
				return a.FriendlyName()
			}
		}
	}
	return "Wi-Fi"
}
