package main

import (
	"encoding/json"
	"reflect"
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
