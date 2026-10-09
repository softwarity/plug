package tun

import (
	"testing"
)

// nxReply is what an upstream says of a name it does not know.
func nxReply(q []byte) []byte {
	r := append([]byte(nil), q...)
	r[2], r[3] = 0x81, 0x83 // QR, RD, RA, NXDOMAIN
	return r
}

// <service>.<namespace>, the way a pod names a Service of another namespace
// and a Helm chart writes it (http://opentelemetry.monitoring:4318): ours when
// a cluster holds it, minted under its long form so the agent dials the
// namespace, its AAAA a NODATA that agrees with the A, and the upstream never
// asked. The macOS search suffix completion, .plug, lands on the same fake.
// Would have caught: the two-label form relayed, NXDOMAIN, and a plugged
// service unable to export its traces with its pod's configuration.
func TestServiceDotNamespaceIsOursWhenAClusterHoldsIt(t *testing.T) {
	tab := newFaketab(fakeBase)
	up := newFakeUpstream(t, nxReply)
	var asked []string
	check := func(name string) bool {
		asked = append(asked, name)
		return name == "opentelemetry.monitoring.svc"
	}
	resp := answerDNS(query("opentelemetry.monitoring", 1), tab, up.dns(), check)
	ip, ok := answerIPv4(resp)
	if !ok || ip&mask24 != fakeBase {
		t.Fatalf("opentelemetry.monitoring: want a fake, got %v", resp)
	}
	if name, _ := tab.lookup(ip); name != "opentelemetry.monitoring.svc" {
		t.Fatalf("the fake must map to the long form the agent dials, got %q", name)
	}
	if asked[0] != "opentelemetry.monitoring.svc" {
		t.Fatalf("the cluster was asked %q, want the long form", asked[0])
	}
	if aaaa := answerDNS(query("opentelemetry.monitoring", 28), tab, up.dns(), check); aaaa == nil || aaaa[3]&0x0f != 0 {
		t.Fatalf("AAAA must be NODATA, agreeing with the A, got %v", aaaa)
	}
	if up.wasAsked() {
		t.Fatal("a name a cluster holds was asked upstream")
	}
	viaSuffix, ok := answerIPv4(answerDNS(query("opentelemetry.monitoring.plug", 1), tab, up.dns(), check))
	if !ok || viaSuffix != ip {
		t.Fatalf("the .plug completion must land on the same fake %s, got %s", ipStr(ip), ipStr(viaSuffix))
	}
}

// Everything else keeps going where it went: a two-label name no cluster
// holds (a name of the local network, odb.lan) goes upstream, and a name under
// a public top-level domain is never even asked of a cluster, so a namespace
// called dev or app cannot take that TLD from the whole machine. .local is
// mDNS's. Would have caught: claiming two-label names on their shape alone.
func TestOtherTwoLabelNamesStayUpstream(t *testing.T) {
	tab := newFaketab(fakeBase)
	var asked []string
	check := func(name string) bool { asked = append(asked, name); return true }
	for _, name := range []string{"x.dev", "github.com", "x.app", "printer.local"} {
		up := newFakeUpstream(t, nxReply)
		resp := answerDNS(query(name, 1), tab, up.dns(), check)
		if resp == nil || resp[3]&0x0f != 3 || !up.wasAsked() {
			t.Fatalf("%s must be relayed upstream as before, got %v", name, resp)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("names that can be public were asked of a cluster: %v", asked)
	}

	deny := func(string) bool { return false }
	up := newFakeUpstream(t, nxReply)
	if resp := answerDNS(query("odb.lan", 1), tab, up.dns(), deny); resp == nil || !up.wasAsked() {
		t.Fatalf("a two-label name no cluster holds must still be asked upstream, got %v", resp)
	}
	up = newFakeUpstream(t, nxReply)
	if resp := answerDNS(query("opentelemetry.monitoring", 1), tab, up.dns(), nil); resp == nil || !up.wasAsked() {
		t.Fatalf("with no cluster to ask, nothing is claimed, got %v", resp)
	}
}

// The checker's fallback: when no agent can answer, a bare name is minted as
// it always was, a dotted one is not. Would have caught: jira.corp minted on
// nobody's word and taken from the whole machine while an agent is down.
func TestNobodyAnsweringMintsBareNamesOnly(t *testing.T) {
	check := newNameChecker(func() []Dialer { return nil }, logfn(func(string, ...any) {}))
	if !check("orders") {
		t.Fatal("a bare name must still be minted when nobody can answer")
	}
	if check("jira.corp.svc") {
		t.Fatal("a dotted name was minted on nobody's word")
	}
}
