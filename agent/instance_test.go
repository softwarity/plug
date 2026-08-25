package agent

import (
	"encoding/json"
	"errors"
	"net"
	"testing"
)

// A served name must point at the INSTANCE holding the session, never at the
// role it plays. These are the two halves of that: where a signpost sends the
// bytes, and how a receipt says who owes it.

// On Swarm the agent is a task of a service, and relayTarget used to answer with
// the SERVICE - i.e. its VIP, load balanced across every task. The session's
// remote-forward lives in one task, so past a single replica the name worked for
// one request in N. A task's container name IS its task name and the overlay's
// DNS answers it with that one task's address, so naming the instance costs
// nothing and the special case is gone.
//
// owner() still answers with the service, and must: it exists to recognise
// ANOTHER agent's signpost, and every replica of one deployment is the same agent.
func TestRelayTargetNamesTheTaskNotTheService(t *testing.T) {
	swarm := selfInfo{name: "neo_plug.1.l5vhiqbv4nqh", service: "neo_plug"}
	if got := swarm.relayTarget(); got != "neo_plug.1.l5vhiqbv4nqh" {
		t.Errorf("a signpost must relay to the task holding the session, got %q", got)
	}
	if got := swarm.owner(); got != "neo_plug" {
		t.Errorf("ownership stays the agent's role across restarts, got %q", got)
	}
	// Off Swarm the container name was always the instance: unchanged, and now
	// the same sentence in both shapes.
	compose := selfInfo{name: "neo-plug-1"}
	if got := compose.relayTarget(); got != "neo-plug-1" {
		t.Errorf("off Swarm the relay target is the container, got %q", got)
	}
}

// The owner address is what any agent dials to find out whether a name is still
// someone's: this instance, and the port that session's forward answers on. No
// port, no instance, no owner - and nobody must read that as "alive".
func TestSessionOwnerIsAnAddressAnyAgentCanDial(t *testing.T) {
	pairs := []portPair{{cluster: "8081", agent: "41017"}, {cluster: "25", agent: "41018"}}
	if got := sessionOwner("neo_plug.1.abc", pairs); got != "neo_plug.1.abc:41017" {
		t.Errorf("owner = %q, want the instance and the session's first port", got)
	}
	if got := sessionOwner("10.244.1.7", pairs); got != "10.244.1.7:41017" {
		t.Errorf("owner = %q, want the pod address and the session's first port", got)
	}
	for _, c := range []struct {
		addr  string
		pairs []portPair
	}{
		{"", pairs},
		{"neo_plug.1.abc", nil},
		{"neo_plug.1.abc", []portPair{{cluster: "8081"}}},
	} {
		if got := sessionOwner(c.addr, c.pairs); got != "" {
			t.Errorf("sessionOwner(%q, %v) = %q, want none: an owner nobody can dial is not an owner", c.addr, c.pairs, got)
		}
	}
	if sessionLive("") {
		t.Error("an empty owner is nobody, and nobody is gone")
	}
}

// On Kubernetes the name is a Service, and a Service with a selector names a
// role: app=plug matches every replica of the agent. The session lives in one
// pod, so the Service carries NO selector and the Endpoints below name the pod.
func TestK8sServiceNamesAPodNotARole(t *testing.T) {
	pairs := []portPair{{cluster: "8081", agent: "41017"}}
	svc := k8sServiceFor("frontend", "10.244.1.7:41017", pairs)

	spec, ok := svc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("no spec in %v", svc)
	}
	if sel, present := spec["selector"]; present {
		t.Errorf("a created Service must carry no selector at all, got %v", sel)
	}
	meta, ok := svc["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("no metadata in %v", svc)
	}
	ann, ok := meta["annotations"].(map[string]string)
	if !ok || ann[sessionOwnerLabel] != "10.244.1.7:41017" {
		t.Errorf("the Service must record WHICH pod serves it, got %v", meta["annotations"])
	}
	labels, ok := meta["labels"].(map[string]string)
	if !ok || labels[k8sManaged] != "plug" {
		t.Errorf("a plug-created Service stays recognisable as one, got %v", meta["labels"])
	}
}

// The Endpoints are the routing decision for a selector-less Service: this pod's
// address, and the port the forward actually listens on (targetPort is never
// consulted without a selector). Their port NAMES must repeat the Service's
// verbatim - k8s pairs them by name, and a mismatch is a name with no route
// rather than a rejected object.
func TestK8sEndpointsNameThisPod(t *testing.T) {
	pairs := []portPair{{cluster: "8081", agent: "41017"}, {cluster: "25", agent: "41018"}}
	ep := k8sEndpointsFor("frontend", "10.244.1.7", pairs)

	subsets, ok := ep["subsets"].([]map[string]any)
	if !ok || len(subsets) != 1 {
		t.Fatalf("one subset, this pod: %v", ep["subsets"])
	}
	addrs, ok := subsets[0]["addresses"].([]map[string]any)
	if !ok || len(addrs) != 1 || addrs[0]["ip"] != "10.244.1.7" {
		t.Fatalf("the name must reach THIS pod and no other, got %v", subsets[0]["addresses"])
	}
	ports, ok := subsets[0]["ports"].([]map[string]any)
	if !ok || len(ports) != len(pairs) {
		t.Fatalf("one endpoint port per exposure, got %v", subsets[0]["ports"])
	}
	svcPorts := k8sPorts(pairs)
	for i, p := range ports {
		if p["port"] != 41017+i {
			t.Errorf("endpoint %d must carry the agent port the forward listens on, got %v", i, p["port"])
		}
		if p["name"] != svcPorts[i]["name"] {
			t.Errorf("endpoint %d is matched to its Service port BY NAME: %v != %v", i, p["name"], svcPorts[i]["name"])
		}
	}
}

// The durability property, from the other side: a workload parked by an instance
// that never comes back used to stay parked for ever, because only the agent that
// wrote a receipt would act on it. The receipt now names its owner, so any agent
// can ask the one question that settles it - is that session still answering? A
// live owner keeps its receipt; a gone one hands it to whoever is still standing.
func TestParkedReceiptIsRestoredOnlyWhenItsOwnerIsGone(t *testing.T) {
	// The session's forward, as any agent sees it: an address that answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	owner := ln.Addr().String()

	original := k8sReceipt{
		Selector: map[string]string{"app.kubernetes.io/name": "frontend"},
		Ports:    json.RawMessage(`[{"name":"http","port":8081,"targetPort":8080}]`),
	}
	raw, err := k8sSignReceipt("", owner, original)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// ANOTHER agent, reading the annotation off the Service it found parked.
	if got := k8sReceiptOwner(raw); got != owner {
		t.Fatalf("the receipt must name its owner, got %q", got)
	}
	if !sessionLive(k8sReceiptOwner(raw)) {
		t.Error("its owner is serving right now: restoring would cut a live session off")
	}

	// The instance is gone: scaled down, rescheduled, replaced. Nobody renews
	// anything and nobody has to - the address simply stops answering.
	ln.Close()
	if sessionLive(k8sReceiptOwner(raw)) {
		t.Error("an owner that no longer answers is gone, and its receipt is anyone's to act on")
	}
	// And the way back is intact for whoever picks it up.
	var back k8sReceipt
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Selector["app.kubernetes.io/name"] != "frontend" || string(back.Ports) != string(original.Ports) {
		t.Errorf("signing a receipt must not disturb what it records, got %+v", back)
	}
}

// A second session taking over the same Service inherits the duty of restoring
// it. Only the FIRST takeover ever saw the original spec, so the owner is
// refreshed and the saved selector and ports are kept exactly as they are -
// re-saving would record the way back to an agent.
func TestK8sSignReceiptRefreshesTheOwnerAndKeepsTheWayBack(t *testing.T) {
	first, err := k8sSignReceipt("", "10.244.1.7:41017", k8sReceipt{
		Selector: map[string]string{"app.kubernetes.io/name": "frontend"},
		Ports:    json.RawMessage(`[{"name":"http","port":8081,"targetPort":8080}]`),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The Service now points at an agent, so THIS is what a second takeover
	// would save if it saved anything.
	second, err := k8sSignReceipt(first, "10.244.2.9:52801", k8sReceipt{
		Selector: nil,
		Ports:    json.RawMessage(`[{"name":"p8081","port":8081,"targetPort":41017}]`),
	})
	if err != nil {
		t.Fatalf("re-sign: %v", err)
	}
	var r k8sReceipt
	if err := json.Unmarshal([]byte(second), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Owner != "10.244.2.9:52801" {
		t.Errorf("the new session owes the restore, got owner %q", r.Owner)
	}
	if r.Selector["app.kubernetes.io/name"] != "frontend" {
		t.Errorf("the original selector must survive every later takeover, got %v", r.Selector)
	}
	if string(r.Ports) != `[{"name":"http","port":8081,"targetPort":8080}]` {
		t.Errorf("the original ports must survive every later takeover, got %s", r.Ports)
	}
}

// Receipts an older agent wrote carry no owner, and there is no migration: an
// owner nobody can dial reads as gone, which is exactly what the gc did with
// every receipt it found before this. An unreadable annotation says the same
// rather than pinning a workload to an owner nobody can check.
func TestReceiptWithoutAnOwnerReadsAsGone(t *testing.T) {
	for _, raw := range []string{
		"",
		`{"selector":{"app":"frontend"},"ports":[]}`,
		`{"selector":`,
	} {
		if own := k8sReceiptOwner(raw); own != "" || sessionLive(own) {
			t.Errorf("receipt %q: owner %q must read as gone", raw, own)
		}
	}
}

// A Service with no selector and no Endpoints resolves to nothing, so what
// happens when the write fails decides whether a cluster keeps working.
//
// The case that matters is an upgrade: `plug update` moves the image and never
// the manifest, so an agent that started refusing on an RBAC without the
// endpoints grant would take -s away from a deployment that worked fine, on an
// update nobody thought twice about. 403 is the one shape that means "deployed
// before this existed"; anything else is a cluster refusing a write it should
// allow, and silently pointing the name at every replica would hide it.
func TestOnlyAMissingGrantFallsBackToTheOldShape(t *testing.T) {
	cases := []struct {
		name string
		code int
		err  error
		want endpointsVerdict
	}{
		{"written", 201, nil, endpointsDone},
		{"replaced", 200, nil, endpointsDone},
		{"no grant, an older deployment", 403, errors.New("forbidden"), endpointsFallback},
		{"rejected by the apiserver", 422, errors.New("invalid"), endpointsFatal},
		{"conflict", 409, errors.New("conflict"), endpointsFatal},
		{"apiserver unreachable", 0, errors.New("dial tcp: connection refused"), endpointsFatal},
		{"gateway error", 502, errors.New("bad gateway"), endpointsFatal},
	}
	for _, c := range cases {
		if got := endpointsOutcome(c.code, c.err); got != c.want {
			t.Errorf("%s: HTTP %d with err=%v gave verdict %d, want %d", c.name, c.code, c.err, got, c.want)
		}
	}
}

// The fallback exists for a deployment that predates the grant, not for one
// that never learned its own address. Both end up in the same place, and both
// have to: a name pointing at every replica is wrong past one replica, but a
// name pointing at nothing is wrong everywhere.
func TestNoAddressAlsoFallsBackRatherThanServingNothing(t *testing.T) {
	// podIP == "" skips the write entirely in k8sPointAtSelf; assert the shape
	// that decision relies on, so a refactor cannot quietly make it fatal.
	if got := endpointsOutcome(403, errors.New("forbidden")); got != endpointsFallback {
		t.Fatalf("a missing grant must fall back, got %d", got)
	}
	if got := endpointsOutcome(0, errors.New("boom")); got == endpointsFallback {
		t.Error("a transport failure must not be mistaken for a missing grant")
	}
}
