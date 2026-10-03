//go:build darwin

package tun

import (
	"os"
	"testing"
)

func TestClusterForPID(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	// Two clusters, each with a registered launcher PID that carries its key.
	// Both pids are LIVE processes (this one and a child), because a marker is
	// only answered for the process that registered it: a number nothing runs
	// under is reaped, not attributed.
	me, child := os.Getpid(), spawnLive(t)
	unregA := RegisterClient("hostA:2222", me, "")
	defer unregA()
	unregB := RegisterClient("hostB:2222", child, "")
	defer unregB()

	if key, ok := clusterForPID(me); !ok || key != "hostA:2222" {
		t.Fatalf("pid %d → %q,%v want hostA:2222,true", me, key, ok)
	}
	if key, ok := clusterForPID(child); !ok || key != "hostB:2222" {
		t.Fatalf("pid %d → %q,%v want hostB:2222,true", child, key, ok)
	}
	// A pid registered to no cluster is not attributed (router then refuses).
	if _, ok := clusterForPID(1); ok {
		t.Fatalf("unknown pid must not be attributed")
	}
	// Once unregistered, the pid stops resolving.
	unregA()
	if _, ok := clusterForPID(me); ok {
		t.Fatalf("unregistered pid must not be attributed")
	}
}
