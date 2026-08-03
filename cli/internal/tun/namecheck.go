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

// checkTTL bounds how long a verdict — found or absent — may be repeated
// without asking the agent again. It used to be five MINUTES for a positive,
// and that number was the enabler of a real poisoning: kill a session
// (Ctrl-C), and for the rest of those five minutes the stub kept telling
// whoever asked that the name existed, minting a fake for it. On a plugged
// workstation running Docker Desktop, "whoever asked" includes the VM — the
// embedded DNS forwards names absent from the cluster upstream, which lands
// here — so a GATEWAY INSIDE THE CLUSTER cached a 198.18.x address that only
// means something on this machine, and stayed broken until restarted.
//
// Five seconds, same as the negative-SOA MINIMUM: plug's answers are honest
// within five seconds, in both directions. The load stays bounded by the OS
// resolver's own cache in front of us — one query per name per TTL — and each
// re-check is one exec on an SSH connection that is already open.
const checkTTL = 5 * time.Second

// newNameChecker builds the pre-mint existence check: ask every current
// transport — present in ANY cluster → mint. When nobody can answer (no
// transport yet, old agents), mint as plug always did — a fake IP whose
// connect is refused with a log, never a hang.
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
		if !found && nxLimiter.allow(name) {
			log.f("tun: %s is in no connected cluster — NXDOMAIN (repeats hidden 30s)", name)
		}
		mu.Lock()
		cache[name] = verdict{found: found, until: time.Now().Add(checkTTL)}
		mu.Unlock()
		return found
	}
}
