//go:build darwin || windows

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// One user's key must not end everyone's datapath.
//
// reconcile dials each active cluster with the key its client registered, and
// every brick on that path (the path guard, the key guard, the owned read) used
// to end in fatal(). The daemon is shared: on macOS it holds the tunnels of
// every session of every account, and it repoints the machine's resolver. A
// `key =` line pointing at a file that is not there, written by one user, took
// the process down for all of them, before the resolver was restored.
//
// The registry root is not reachable from this package (it is /var/run/plug on
// macOS, root-owned, and the tun package keeps it private), so this exercises
// the two halves reconcile is made of: the dial itself, with the key absent,
// and what applyDial does with the refusal that comes back.
func TestAMissingProfileKeyDoesNotEndTheDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := config{host: "cluster.example", port: "2222", key: filepath.Join(home, "absent.key")}
	key := cfg.host + ":" + cfg.port

	// The dial returns. That it returns AT ALL is the assertion: this very test
	// process is what fatal would have exited.
	tr, err := dialTunnel(cfg)
	if err == nil {
		tr.Close()
		t.Fatal("a profile whose key file does not exist dialled anyway")
	}
	if !strings.Contains(err.Error(), cfg.key) {
		t.Errorf("the refusal does not name the key file, so the person cannot fix the profile line: %v", err)
	}
	if !isLocalRefusal(err) {
		t.Errorf("an absent key is a condition of this machine, and must be recognised as one: %T %v", err, err)
	}

	// And the daemon records it as THIS cluster's failure: no tunnel installed,
	// the reason kept for the waiting launcher, and the cluster backed off the
	// way an agent's "not authorized" is, rather than re-dialled three times a
	// second with the same answer.
	tunnels := map[string]*tunnel.Transport{}
	delete(authRefused, key)
	defer delete(authRefused, key)
	applyDial(tun.NewClusterTransports(), tunnels, dialOutcome{key: key, err: err})
	if len(tunnels) != 0 {
		t.Errorf("a refused dial installed a tunnel: %v", tunnels)
	}
	if _, backedOff := authRefused[key]; !backedOff {
		t.Error("a local refusal is retried on every tick: it will not change until a file does, and the log fills")
	}
}
