//go:build darwin || windows

package main

import (
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The teardown must not touch `tunnels` while a reconcile tick is still in
// flight. Closing stop does not end the loop at once: a tick can be inside
// dialTunnel for the whole dial timeout and writes to the map on its way out,
// so tearing down concurrently is a plain data race — and the process it kills
// is the root daemon holding the machine's DNS, which then dies before
// restoring the resolver.
//
// Under -race this fails outright if reconcileLoop stops signalling completion
// and the teardown goes back to racing it. Without -race it is merely flaky,
// which is exactly why CI runs the detector.
func TestTeardownWaitsForTheReconcileLoop(t *testing.T) {
	tunnels := map[string]*tunnel.Transport{}
	stop := make(chan struct{})
	done := reconcileLoop(nil, tunnels, stop)

	// Let the loop reach its select and tick at least once (300ms period).
	time.Sleep(400 * time.Millisecond)

	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcileLoop never signalled it had finished — the teardown would race it")
	}

	// Only now is the map ours: what closeAll does, without a live transport.
	for k := range tunnels {
		delete(tunnels, k)
	}
}
