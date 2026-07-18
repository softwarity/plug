package tun

import (
	"sync"
	"time"
)

// clusterNameResolver is the optional facet of a Dialer that can ask its agent
// whether a bare name exists in that cluster (tunnel.Transport implements it).
// ok=false means the transport cannot answer (an agent that predates the
// `resolve` verb) — the checker then falls back to minting, the pre-check
// behaviour.
type clusterNameResolver interface {
	ResolveInCluster(name string) (found, ok bool)
}

// nameChecker answers "should this bare name be minted?" — false turns the DNS
// reply into an honest NXDOMAIN.
type nameChecker func(name string) bool

// nxLimiter throttles the NXDOMAIN log line — the very leak this check exists
// for (a Docker Desktop VM forwarding its containers' unknown lookups here)
// can ask in bursts.
var nxLimiter = newLogLimiter(30 * time.Second)

// newNameChecker builds the pre-mint existence check: ask every current
// transport — present in ANY cluster → mint. Verdicts are cached (found 5 min;
// absent 30 s, so a service being deployed right now appears quickly). When
// nobody can answer (no transport yet, old agents), mint as plug always did —
// a fake IP whose connect is refused with a log, never a hang.
func newNameChecker(dialers func() []Dialer, log logfn) nameChecker {
	type verdict struct {
		found bool
		until time.Time
	}
	var mu sync.Mutex
	cache := map[string]verdict{}
	return func(name string) bool {
		mu.Lock()
		if v, hit := cache[name]; hit && time.Now().Before(v.until) {
			mu.Unlock()
			return v.found
		}
		mu.Unlock()

		answered, found := false, false
		for _, d := range dialers() {
			cr, is := d.(clusterNameResolver)
			if !is {
				continue
			}
			f, ok := cr.ResolveInCluster(name)
			if !ok {
				continue
			}
			answered = true
			if f {
				found = true
				break
			}
		}
		if !answered {
			return true
		}
		ttl := 5 * time.Minute
		if !found {
			ttl = 30 * time.Second
			if nxLimiter.allow(name) {
				log.f("tun: %s is in no connected cluster — NXDOMAIN (repeats hidden 30s)", name)
			}
		}
		mu.Lock()
		cache[name] = verdict{found: found, until: time.Now().Add(ttl)}
		mu.Unlock()
		return found
	}
}
