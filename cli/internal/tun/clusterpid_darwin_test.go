//go:build darwin

package tun

import "testing"

func TestClusterForPID(t *testing.T) {
	graftDir = t.TempDir()

	// Two clusters, each with a registered launcher PID that carries its key.
	unregA := RegisterClient("hostA:2222", 4242)
	defer unregA()
	unregB := RegisterClient("hostB:2222", 5353)
	defer unregB()

	if key, ok := clusterForPID(4242); !ok || key != "hostA:2222" {
		t.Fatalf("pid 4242 → %q,%v want hostA:2222,true", key, ok)
	}
	if key, ok := clusterForPID(5353); !ok || key != "hostB:2222" {
		t.Fatalf("pid 5353 → %q,%v want hostB:2222,true", key, ok)
	}
	// A pid registered to no cluster is not attributed (router then refuses).
	if _, ok := clusterForPID(9999); ok {
		t.Fatalf("unknown pid must not be attributed")
	}
	// Once unregistered, the pid stops resolving.
	unregA()
	if _, ok := clusterForPID(4242); ok {
		t.Fatalf("unregistered pid must not be attributed")
	}
}
