package agent

import (
	"encoding/json"
	"testing"
)

// A taken-over Service keeps the name it gave each port, so an Ingress that
// names the port (and the ingress controller's endpoint-to-port matching) still
// resolve a backend. Renaming the port to "p<port>" was reachable through
// kube-proxy (it routes by number) but not through an Ingress, which is a
// WebSocket that hangs in the browser while the name answers inside the cluster.
func TestNamedPairsKeepsTheServicePortName(t *testing.T) {
	raw := json.RawMessage(`[{"name":"http","port":3000,"targetPort":"http"},{"name":"metrics","port":9090,"targetPort":9090}]`)
	pairs := []portPair{{cluster: "3000", agent: "39359"}, {cluster: "9090", agent: "40001"}}
	got := k8sNamedPairs(pairs, raw)
	if got[0].name != "http" || k8sPortName(got[0]) != "http" {
		t.Fatalf("port 3000 should keep the name http, got %q / %q", got[0].name, k8sPortName(got[0]))
	}
	if got[1].name != "metrics" || k8sPortName(got[1]) != "metrics" {
		t.Fatalf("port 9090 should keep the name metrics, got %q", got[1].name)
	}
	// Both k8sPorts and k8sEndpointsFor must publish that same name, or the
	// Service port and its endpoint do not match.
	svcPorts := k8sPorts(got)
	if svcPorts[0]["name"] != "http" || svcPorts[0]["port"] != 3000 {
		t.Fatalf("Service port not kept named: %v", svcPorts[0])
	}
	ep := k8sEndpointsFor("fpl-svc", "10.1.2.3", got)
	subset := ep["subsets"].([]map[string]any)[0]
	if subset["ports"].([]map[string]any)[0]["name"] != "http" {
		t.Fatalf("endpoint port not named http: %v", subset["ports"])
	}
}

// A plug-created name (no Service to take over) and a port the Service does not
// carry both fall back to "p<port>": plug owns both sides and names them.
func TestNamedPairsFallsBackToPPort(t *testing.T) {
	if k8sPortName(portPair{cluster: "3000", agent: "39359"}) != "p3000" {
		t.Fatal("a nameless pair must fall back to p3000")
	}
	// A Service that carries a DIFFERENT port leaves ours nameless.
	got := k8sNamedPairs([]portPair{{cluster: "3000", agent: "39359"}}, json.RawMessage(`[{"name":"http","port":8080}]`))
	if got[0].name != "" || k8sPortName(got[0]) != "p3000" {
		t.Fatalf("an unmatched port must stay p3000, got %q", got[0].name)
	}
	// No ports at all, and unreadable JSON: the pairs come back untouched.
	if k8sNamedPairs([]portPair{{cluster: "3000"}}, nil)[0].name != "" {
		t.Fatal("nil ports must leave the pair nameless")
	}
	if k8sNamedPairs([]portPair{{cluster: "3000"}}, json.RawMessage(`not json`))[0].name != "" {
		t.Fatal("unreadable ports must leave the pair nameless")
	}
}
