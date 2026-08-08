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
	// A lookup already on the wire, so N concurrent questions about ONE name
	// cost one round trip instead of N.
	//
	// The cache alone does not cover this: it only helps once an answer is
	// back. Windows asks about the same name several times AT ONCE — its search
	// suffix turns `svc` into a query for `svc.plug` and one for `svc`, both
	// landing on this same key, and its resolver re-sends after about a second
	// while nothing has answered yet. Each of those used to open its own
	// session and wait out the agent's budget, and an ABSENT name costs that
	// budget in full by definition: the agent cannot say "no" before it has
	// finished looking. Stacked up, they outlasted what the client would wait —
	// one leg gave up resolving after 8s on a name plug decides in under two.
	type flight struct {
		done  chan struct{}
		found bool
	}
	var mu sync.Mutex
	cache := map[string]verdict{}
	inflight := map[string]*flight{}
	return func(name string) bool {
		mu.Lock()
		if v, hit := cache[name]; hit && time.Now().Before(v.until) {
			mu.Unlock()
			return v.found
		}
		if f, busy := inflight[name]; busy {
			mu.Unlock()
			<-f.done
			return f.found
		}
		f := &flight{done: make(chan struct{})}
		inflight[name] = f
		mu.Unlock()

		started := time.Now()
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
		took := time.Since(started)

		// Nobody could answer (no transport yet, an agent too old): mint, as
		// plug always did, and do NOT cache a verdict we never got.
		result := true
		mu.Lock()
		delete(inflight, name)
		if answered {
			result = found
			cache[name] = verdict{found: found, until: time.Now().Add(checkTTL)}
		}
		mu.Unlock()

		if answered && !found && nxLimiter.allow(name) {
			// The duration is here because it is the number that decides
			// whether a slow NXDOMAIN is the agent thinking or the link.
			log.f("tun: %s is in no connected cluster — NXDOMAIN in %s (repeats hidden 30s)", name, took.Round(time.Millisecond))
		}
		f.found = result
		close(f.done)
		return result
	}
}
