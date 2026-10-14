//go:build darwin || windows

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// A tunnel the agent cut and refuses back is dropped on the next tick even
// when no plug is attached to it at that moment: the session whose flow found
// the cut has usually ended already. Left in place, the tunnel stayed ready
// through its grace and the next plug ran on it without hearing why.
func TestARefusedTunnelIsDroppedWithNoClientAttached(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	agent, host, port := startInviteAgent(t, map[string]string{})
	cfg := loadProfile("demo")
	cfg.host, cfg.port = host, port
	tr, err := dialAgent(&cfg)
	if err != nil {
		t.Fatalf("the first member was refused: %v", err)
	}
	defer tr.Close()
	key := host + ":" + port
	tunnels := map[string]*tunnel.Transport{key: tr}
	ct := tun.NewClusterTransports()
	ct.Set(key, tr)
	t.Cleanup(func() {
		delete(authRefused, key)
		delete(authRetryIn, key)
		tun.ClearClusterError(key)
		tun.UnmarkClusterReady(key)
	})

	agent.mu.Lock()
	var fp string
	for k := range agent.members {
		fp = k
	}
	agent.mu.Unlock()
	agent.revoke(fp)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c, err := tr.DialContext(ctx, "tcp", "anything:80"); err == nil {
		c.Close()
		t.Fatal("a revoked member's transport still opened a connection")
	}

	dropRefused(ct, tunnels)
	if _, kept := tunnels[key]; kept {
		t.Fatal("the refused tunnel is still held: the next plug would run on it")
	}
	if ct.Has(key) {
		t.Fatal("the datapath still routes through the refused tunnel")
	}
	if _, backedOff := authRefused[key]; !backedOff {
		t.Error("the refusal was not recorded: the daemon would re-dial at once and the launcher has no reason to show")
	}
	if msg := tun.ClusterError(key); msg != "" && !strings.Contains(msg, "by approval") {
		t.Errorf("the reason left for the launcher is not the agent's: %q", msg)
	}
}
