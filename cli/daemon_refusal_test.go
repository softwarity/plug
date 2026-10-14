//go:build darwin || windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A refusal is asked again when someone runs plug for the cluster again. The
// newcomer to an agent that admits by approval is refused, a member accepts the
// key, and the newcomer's next plug must reach the agent, not the answer the
// daemon kept from a minute ago. A cluster that stays in use keeps its backoff.
func TestARefusalIsAskedAgainWhenPlugRunsAgain(t *testing.T) {
	const key = "cluster.example:2222"
	saved := activeLastTick
	defer func() { activeLastTick = saved; delete(authRefused, key) }()

	authRefused[key] = time.Now()
	activeLastTick = map[string]bool{key: true}
	askAgainOnReturn(key)
	if _, kept := authRefused[key]; !kept {
		t.Error("a cluster still in use forgot its refusal: the agent would be asked three times a second")
	}
	activeLastTick = map[string]bool{}
	askAgainOnReturn(key)
	if _, kept := authRefused[key]; kept {
		t.Error("a plug run after the cluster was idle got the refusal the daemon kept, without asking the agent")
	}
}

// A key waiting for a member is asked about again within seconds, so the
// newcomer accepted a moment ago gets in on their next plug; every other
// refusal keeps the minute.
func TestAWaitingKeyIsAskedAboutAgainSoon(t *testing.T) {
	pending := &tunnel.AuthFailure{Addr: "h:2230", Admission: "pending fp=j7Q/a6O4RTKp members=alice@laptop"}
	if got := retryAfter(pending); got != pendingRetryAfter {
		t.Errorf("a pending key waits %v, want %v", got, pendingRetryAfter)
	}
	if got := retryAfter(&tunnel.AuthFailure{Addr: "h:2222"}); got != authRetryAfter {
		t.Errorf("a key refused outright waits %v, want %v", got, authRetryAfter)
	}
}

// A cluster coming up flushes the system's resolver cache before the launcher
// is told it is ready, so a name answered NXDOMAIN while the cluster was out of
// reach is asked again. A dial that failed changes no name and flushes nothing.
func TestATunnelComingUpFlushesTheSystemsNegativeAnswers(t *testing.T) {
	flushes := 0
	was := flushSystemDNS
	flushSystemDNS = func() { flushes++ }
	t.Cleanup(func() { flushSystemDNS = was })

	key := "flush.example:2230"
	tunnels := map[string]*tunnel.Transport{}
	ct := tun.NewClusterTransports()
	t.Cleanup(func() {
		delete(authRefused, key)
		delete(authRetryIn, key)
		tun.ClearClusterError(key)
		tun.UnmarkClusterReady(key)
	})

	applyDial(ct, tunnels, dialOutcome{key: key, err: errors.New("dial tcp: connection refused")})
	if flushes != 0 {
		t.Fatalf("a failed dial flushed the resolver cache %d time(s)", flushes)
	}
	applyDial(ct, tunnels, dialOutcome{key: key, tr: &tunnel.Transport{}})
	if flushes != 1 {
		t.Fatalf("the tunnel came up and the resolver cache was flushed %d time(s), want 1", flushes)
	}
}
