//go:build windows

package tun

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Round-trip of the NRPT rule (the Windows equivalent of scutil/resolv.conf):
// set writes the DnsPolicyConfig key with our server, clear reaps every rule
// carrying that server. Uses a throwaway dnsIP so a live plug rule on the
// machine is never touched. Needs elevation (HKLM) — skipped without it.
func TestNRPTRoundTrip(t *testing.T) {
	const dnsIP = "198.18.77.53" // test-only — clear is keyed on this exact server
	if err := setSystemNRPT(dnsIP); err != nil {
		t.Skipf("setSystemNRPT needs elevation: %v", err)
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+nrptRuleName, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("rule key missing after set: %v", err)
	}
	servers, _, err := k.GetStringValue("GenericDNSServers")
	k.Close()
	if err != nil || servers != dnsIP {
		t.Fatalf("GenericDNSServers = %q, %v — want %q", servers, err, dnsIP)
	}
	clearSystemNRPT(dnsIP)
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+nrptRuleName, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Fatal("rule key still present after clear")
	}
}
