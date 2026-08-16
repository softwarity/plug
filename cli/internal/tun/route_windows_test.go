//go:build windows

package tun

import (
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// nrptWritable: these tests write HKLM, which needs elevation. Off a runner that
// is a legitimate skip; ON one it is not — a GitHub Windows runner is elevated,
// so a refusal there means the test never ran, and `go test` prints no skip
// without -v. A security property nobody exercises is worth less than no test at
// all, because it reads as covered.
func nrptWritable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("cannot write the NRPT policy key on a runner that should be elevated: %v", err)
	}
	t.Skipf("needs elevation (HKLM): %v", err)
}

// Round-trip of the NRPT rule (the Windows equivalent of scutil/resolv.conf):
// set writes the DnsPolicyConfig key with our server, clear reaps every rule
// carrying that server. Uses a throwaway dnsIP so a live plug rule on the
// machine is never touched. Needs elevation (HKLM) — skipped without it.
func TestNRPTRoundTrip(t *testing.T) {
	const dnsIP = "198.18.77.53" // test-only — clear is keyed on this exact server
	nrptWritable(t, setSystemNRPT(dnsIP))
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

// The rule plug must never touch: the one a corporate VPN client writes.
//
// On Windows a VPN client routes its own suffixes with NRPT rules of its own —
// `*.corp.example` to the resolver behind the tunnel. plug writes a rule too,
// for its `.plug` suffix, and clears rules on the way out. If it cleared one of
// theirs, the user would lose DNS to their whole intranet, with no reason on
// earth to suspect plug: the failure is total, silent, and lands on someone who
// only started a dev session.
//
// The protection exists — clearSystemNRPT is keyed on OUR server address, so a
// rule pointing anywhere else does not match — but nothing proved it, and a
// property that holds by accident holds until someone refactors.
func TestNRPTLeavesAForeignRuleAlone(t *testing.T) {
	const ours = "198.18.77.53"  // test-only, as above
	const theirs = "10.77.88.99" // what a corporate client would point at
	const foreignRule = `{0F1E2D3C-4B5A-6978-8796-A5B4C3D2E1F0}`
	const foreignSuffix = ".corp.example"

	fk, _, err := registry.CreateKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+foreignRule, registry.SET_VALUE)
	nrptWritable(t, err)
	_ = fk.SetDWordValue("Version", 2)
	_ = fk.SetStringsValue("Name", []string{foreignSuffix})
	_ = fk.SetStringValue("GenericDNSServers", theirs)
	_ = fk.SetDWordValue("ConfigOptions", 0x8)
	fk.Close()
	defer registry.DeleteKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+foreignRule)

	intact := func(when string) {
		t.Helper()
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+foreignRule, registry.QUERY_VALUE)
		if err != nil {
			t.Fatalf("%s: the foreign NRPT rule is GONE — plug deleted a policy that is not its own: %v", when, err)
		}
		defer k.Close()
		if got, _, _ := k.GetStringValue("GenericDNSServers"); got != theirs {
			t.Fatalf("%s: the foreign rule now points at %q, want %q", when, got, theirs)
		}
		if names, _, _ := k.GetStringsValue("Name"); len(names) != 1 || names[0] != foreignSuffix {
			t.Fatalf("%s: the foreign rule now covers %v, want [%s]", when, names, foreignSuffix)
		}
	}

	// setSystemNRPT clears before it writes — the first place a foreign rule
	// could be swept away.
	nrptWritable(t, setSystemNRPT(ours))
	intact("after plug installed its own rule")

	clearSystemNRPT(ours)
	intact("after plug removed its own rule")

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+nrptRuleName, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Fatal("plug's own rule survived its own clear — it must take its rule and only its rule")
	}
}
