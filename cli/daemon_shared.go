//go:build darwin || windows

package main

import (
	"net"
	"sync"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// globalKey is the pseudo-cluster key under which the ONE global datapath (a detached
// daemon on macOS, an SCM service on Windows) holds the datapath for ALL clusters and
// routes each flow to the right tunnel by PID at connect (docs/multicluster.md). No
// real cluster key is ever "@global".
const globalKey = "@global"

// tunnelGrace keeps a cluster's tunnel open briefly after its last client goes away,
// so back-to-back `plug`s of that cluster reuse it (a ~0.2 s run) instead of
// re-dialing (~1 s). Idle timers live here; the datapath as a whole is still reaped
// after globalKey's longer grace once no cluster has any client at all.
const tunnelGrace = 20 * time.Second

var tunnelIdleSince = map[string]time.Time{}

// Clusters whose agent refused every key we have, and when it last said so. The
// reconcile ticker runs three times a second; a permanent refusal must not
// become three handshakes a second for the length of a working day.
var authRefused = map[string]time.Time{}

// How long to take an agent at its word before asking again. Long enough that a
// refusal is not a load generator, short enough that enrolling a key does not
// mean hunting down a daemon to restart.
const authRetryAfter = 60 * time.Second

// applyDial records a finished dial. Called only from the reconcile goroutine,
// so every map it touches has a single owner.
func applyDial(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, r dialOutcome) {
	if r.err != nil {
		if tunnel.IsAuthFailure(r.err) {
			authRefused[r.key] = time.Now()
		}
		info("daemon: connect %s: %v", r.key, r.err)
		tun.MarkClusterError(r.key, r.err.Error()) // surface the reason to a waiting launcher
		return
	}
	if _, have := tunnels[r.key]; have {
		// Two dials for one cluster cannot happen through dialSet, but a cluster
		// that went away and came back between the dial and its result can. Keep
		// the one already installed and close the latecomer rather than leak it.
		r.tr.Close()
		return
	}
	delete(authRefused, r.key)
	tunnels[r.key] = r.tr
	ct.Set(r.key, r.tr)
	tun.ClearClusterError(r.key)
	tun.MarkClusterReady(r.key)
	info("daemon: tunnel up for %s", r.key)
}

// dialSet tracks which clusters have a dial in flight, so the reconcile loop can
// start one and move on instead of waiting for it.
//
// The loop ticks three times a second and used to dial inline, with a 15s
// timeout on the dial. A cluster whose agent was down parked the WHOLE loop for
// those fifteen seconds: no other cluster got its tunnel, no dead tunnel got
// closed, and the next tick simply waited. On a machine running several agents -
// the only machine this loop exists for - one of them being unreachable froze
// the rest.
//
// The set is what stops the other failure that shape invites: at three ticks a
// second, a dial that takes fifteen seconds to fail would have forty-five copies
// of itself running by the time the first one gave up.
type dialSet struct {
	mu sync.Mutex
	in map[string]bool
}

func newDialSet() *dialSet { return &dialSet{in: map[string]bool{}} }

// begin runs f for key unless a run for that key is already going, and forgets
// the key when it ends. Returns whether it started one.
func (d *dialSet) begin(key string, f func()) bool {
	d.mu.Lock()
	if d.in[key] {
		d.mu.Unlock()
		return false
	}
	d.in[key] = true
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.in, key)
			d.mu.Unlock()
		}()
		f()
	}()
	return true
}

func (d *dialSet) running(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.in[key]
}

// reconcileOnce opens a tunnel for each active cluster missing one and closes tunnels
// whose cluster no longer has a live client. Each open/close flips the cluster's ready
// marker so `plug -p X <cmd>` can wait for its own tunnel. Shared by macOS and Windows
// (both hold ONE datapath + a tunnel per active cluster; only the process model — a
// detached daemon vs an SCM service — differs).
// dialOutcome carries a finished dial back to the goroutine that owns `tunnels`.
// Every map in this file is touched by the reconcile goroutine and only by it;
// the dial itself is all that runs elsewhere.
type dialOutcome struct {
	key string
	tr  *tunnel.Transport
	err error
}

var (
	dials   = newDialSet()
	dialOut = make(chan dialOutcome, 32)
)

// reconcileOnce with wait=true dials inline, which is what the FIRST call wants:
// the daemon signals ready once the cluster that started it has its tunnel, so a
// `plug <cmd>` waiting behind it does not race the first connect. Every later
// call comes from the ticker and passes false.
func reconcileOnce(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	reconcile(ct, tunnels, true)
}

func reconcile(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, wait bool) {
	// Whatever finished since the last tick, applied here rather than in the
	// goroutine that dialled: `tunnels`, `authRefused` and the marker calls all
	// belong to this one.
	for draining := true; draining; {
		select {
		case r := <-dialOut:
			applyDial(ct, tunnels, r)
		default:
			draining = false
		}
	}
	active := map[string]bool{}
	for _, key := range tun.ActiveClusters() {
		active[key] = true
		if _, have := tunnels[key]; have {
			continue
		}
		host, port, err := net.SplitHostPort(key)
		if err != nil {
			host, port = key, ""
		}
		// The daemon holds the tunnel, but the IDENTITY is the client's: it
		// registered the profile key it wants offered. Dialling without it meant
		// the built-in key alone, and an enrolled developer refused.
		if refusedAt, seen := authRefused[key]; seen {
			// An agent that refused these keys will refuse them again. Retrying
			// three times a second turned a stated reason ("key … is not
			// authorized") into a session that merely felt slow and then failed
			// somewhere else entirely. Re-check rarely, in case a key was enrolled
			// while this daemon kept running.
			if time.Since(refusedAt) < authRetryAfter {
				continue
			}
		}
		cfg := config{host: host, port: port, key: tun.ClusterKeyFile(key)}
		if wait {
			tr, err := dialTunnel(cfg)
			applyDial(ct, tunnels, dialOutcome{key: key, tr: tr, err: err})
			continue
		}
		// Started and left to run. The dial carries a 15s timeout, and holding
		// the loop for it meant every OTHER cluster waited on the one that was
		// down: no tunnel opened, no dead tunnel closed, the next tick simply
		// late. dialSet is what keeps three ticks a second from piling up
		// forty-five copies of the same failing dial.
		k := key
		dials.begin(k, func() {
			tr, err := dialTunnel(cfg)
			dialOut <- dialOutcome{key: k, tr: tr, err: err}
		})
	}
	for key, tr := range tunnels {
		if active[key] {
			delete(tunnelIdleSince, key) // in use — reset the idle timer
			continue
		}
		// No live client. Hold the tunnel through tunnelGrace so back-to-back `plug`s
		// of the same cluster reuse it instead of paying a fresh dial each time.
		if tunnelIdleSince[key].IsZero() {
			tunnelIdleSince[key] = time.Now()
			continue
		}
		if time.Since(tunnelIdleSince[key]) < tunnelGrace {
			continue
		}
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
		delete(tunnelIdleSince, key)
		info("daemon: tunnel down for %s", key)
	}
}

// reconcileLoop re-syncs the tunnel set with the active clusters. It polls often so a
// just-registered client's tunnel opens near-instantly (that open is what the launcher
// waits on); tunnelGrace, not the tick, governs how long an idle tunnel lives.
// reconcileLoop keeps the tunnel set matching the live clusters until stop is
// closed. It returns a channel closed when the loop has REALLY finished, which
// the teardown must wait on before touching `tunnels`.
//
// Closing stop is not enough: a tick already in flight can sit inside
// dialTunnel for the whole dial timeout, and it writes to the map on the way
// out. Tearing down concurrently was a plain data race — "concurrent map
// iteration and map write" kills the process, and this one runs as root holding
// the machine's DNS, so it died before restoring the resolver. The race
// detector, now on in CI, is what keeps this honest.
func reconcileLoop(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(300 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				reconcile(ct, tunnels, false)
			}
		}
	}()
	return done
}

// reapGlobal stops the datapath once no cluster has had a live client for grace —
// long enough to ride through a kill+relaunch of a process.
func reapGlobal(dp *tun.Datapath, stop <-chan struct{}) {
	const grace = 30 * time.Second
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var emptySince time.Time
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if len(tun.ActiveClusters()) > 0 {
				emptySince = time.Time{}
				continue
			}
			if emptySince.IsZero() {
				emptySince = time.Now()
				continue
			}
			if time.Since(emptySince) >= grace {
				dp.Stop()
				return
			}
		}
	}
}

// closeAll drops every tunnel. The caller MUST have waited for reconcileLoop's
// done channel first — nothing else guards this map.
func closeAll(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	for key, tr := range tunnels {
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
	}
}
