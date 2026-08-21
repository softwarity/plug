package main

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

// The reclaim branch used to write a bare {app: plug}. RFC 7386 MERGES maps, so
// on a Service whose selector carried anything else, that key was ADDED to the
// originals rather than replacing them — leaving a selector that demands
// app=plug AND app.kubernetes.io/name=<workload>, which matches no pod. Zero
// endpoints, and every connection to the name times out. Both branches now build
// the patch here, so they cannot drift apart again.
func TestK8sRepointPatchReplacesTheWholeSelector(t *testing.T) {
	pairs := []portPair{{cluster: "8081", agent: "41017"}}
	// A Helm-deployed Service: the labels that produced the real failure.
	current := map[string]string{
		"app.kubernetes.io/instance": "flight-folder-frontend",
		"app.kubernetes.io/name":     "flight-folder-frontend",
	}
	patch := k8sRepointPatch(pairs, current, map[string]any{"plug.linger.since": nil})

	spec, ok := patch["spec"].(map[string]any)
	if !ok {
		t.Fatalf("no spec in %v", patch)
	}
	sel, ok := spec["selector"].(map[string]any)
	if !ok {
		t.Fatalf("no selector in %v", spec)
	}
	if sel["app"] != "plug" {
		t.Errorf("selector must point at the agent, got %v", sel["app"])
	}
	for k := range current {
		v, present := sel[k]
		if !present || v != nil {
			t.Errorf("%s must be explicitly nulled or it survives the merge — got %v (present=%v)", k, v, present)
		}
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
