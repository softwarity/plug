package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// selectorPatch feeds an RFC 7386 merge patch, where object keys MERGE — so
// replacing a selector requires explicitly nulling every current key the target
// doesn't carry. A half-merged selector ({app: plug, tier: front}) matches no
// pod at all: these cases are the takeover's correctness core.
func TestSelectorPatch(t *testing.T) {
	cases := []struct {
		name            string
		target, current map[string]string
		want            map[string]any
	}{
		{
			name:    "takeover nulls the extra keys",
			target:  map[string]string{"app": "plug"},
			current: map[string]string{"app": "webapp", "tier": "front"},
			want:    map[string]any{"app": "plug", "tier": nil},
		},
		{
			name:    "restore puts the original back over the plug selector",
			target:  map[string]string{"app": "webapp", "tier": "front"},
			current: map[string]string{"app": "plug"},
			want:    map[string]any{"app": "webapp", "tier": "front"},
		},
		{
			name:    "same keys — plain replace, nothing nulled",
			target:  map[string]string{"app": "plug"},
			current: map[string]string{"app": "webapp"},
			want:    map[string]any{"app": "plug"},
		},
	}
	for _, c := range cases {
		if got := selectorPatch(c.target, c.current); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: selectorPatch(%v, %v) = %v, want %v", c.name, c.target, c.current, got, c.want)
		}
	}
}

// The k8s parking receipt must round-trip selector AND ports byte-for-byte —
// ports travel as raw JSON so the restore re-patches exactly what was there.
func TestK8sReceiptRoundTrip(t *testing.T) {
	in := k8sReceipt{
		Selector: map[string]string{"app": "webapp", "tier": "front"},
		Ports:    json.RawMessage(`[{"port":8080,"targetPort":8080,"name":"http"}]`),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out k8sReceipt
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out.Selector, in.Selector) || string(out.Ports) != string(in.Ports) {
		t.Fatalf("round-trip: %+v != %+v", out, in)
	}
}

// Two Service shapes accept the takeover's patch and then carry nothing: a
// headless Service (no virtual IP, so targetPort is never applied) and an
// ExternalName (no endpoints at all). Both used to surface as a 90-second
// timeout blaming the cluster's scheduler, so the refusal has to happen while
// the agent still holds the object.
func TestK8sUnroutable(t *testing.T) {
	cases := []struct {
		name           string
		typ, clusterIP string
		want           string // substring the refusal must name, "" to accept
	}{
		{"ordinary ClusterIP is routable", "ClusterIP", "10.96.0.42", ""},
		{"NodePort is routable too", "NodePort", "10.96.0.43", ""},
		{"LoadBalancer is routable too", "LoadBalancer", "10.96.0.44", ""},
		{"headless has no virtual IP", "ClusterIP", "None", "headless"},
		{"ExternalName carries no endpoints", "ExternalName", "", "ExternalName"},
		// The API omits `type` on nothing, but a partial read must not turn into
		// a refusal: absence of evidence is not a headless Service.
		{"unknown shape is accepted", "", "10.96.0.45", ""},
	}
	for _, c := range cases {
		got := k8sUnroutable("frontend", c.typ, c.clusterIP)
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: refused a routable Service — %q", c.name, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s: refusal must name %q, got %q", c.name, c.want, got)
		case c.want != "" && !strings.Contains(got, "frontend"):
			t.Errorf("%s: refusal must name the Service, got %q", c.name, got)
		}
	}
}

// The repoint used to write a selector, and both of its shapes were wrong in
// turn. A bare {app: plug} MERGED (RFC 7386) into a Helm Service's selector,
// leaving one that demands app=plug AND app.kubernetes.io/name=<workload> and
// matches no pod: zero endpoints, every connection timing out. Nulling the other
// keys fixed that, and left the deeper one - `app: plug` matches every replica of
// the agent, while the session lives in exactly one pod.
//
// So the patch now DELETES the selector, which is what puts the Service's
// Endpoints in the agent's hands (k8sEndpointsFor), and neither shape of the bug
// can be built any more: there is no selector left to half-merge or to spread
// across replicas.
func TestK8sRepointPatchDropsTheSelector(t *testing.T) {
	pairs := []portPair{{cluster: "8081", agent: "41017"}}
	patch := k8sRepointPatch(pairs, map[string]any{"plug.linger.since": nil})

	spec, ok := patch["spec"].(map[string]any)
	if !ok {
		t.Fatalf("no spec in %v", patch)
	}
	sel, present := spec["selector"]
	if !present || sel != nil {
		t.Errorf("the selector must be explicitly nulled (a merge patch deletes a key by null), got %v (present=%v)", sel, present)
	}
	// The ports go with it: the forward listens on an allocated port, never on
	// the cluster port the original Service targeted.
	ports, ok := spec["ports"].([]map[string]any)
	if !ok || len(ports) != 1 {
		t.Fatalf("ports must be replaced wholesale, got %v", spec["ports"])
	}
	if ports[0]["port"] != 8081 || ports[0]["targetPort"] != 41017 {
		t.Errorf("port must map the cluster port to the allocated one, got %v", ports[0])
	}
}
