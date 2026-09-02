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
		var resolvers []clusterNameResolver
		for _, d := range dialers() {
			if cr, is := d.(clusterNameResolver); is {
				resolvers = append(resolvers, cr)
			}
		}
		found, answered := askEveryCluster(resolvers, name)
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

// askEveryCluster asks all of them at once and reports whether any holds the
// name, and whether any could answer at all.
//
// It used to walk them in turn, and each question is bounded at three seconds by
// the agent's own budget, so a lookup for an unknown short name cost three
// seconds per cluster that was slow to reply: nine, on a laptop attached to three
// clusters where two were reachable but sluggish. This sits on the resolution
// path, so that time is paid by whatever the user just typed.
//
// Parallel rather than staggered, unlike the upstream DNS race next door, and the
// difference is worth naming: there, every extra server asked is extra traffic to
// somebody else's resolver. Here each cluster is asked exactly one question
// either way, and they are different clusters. Nothing is saved by asking them
// one after another.
//
// A cluster holding the name ends it immediately. Otherwise every answer is
// waited for, because "nobody has it" and "nobody could answer" lead to different
// decisions upstream, and only counting the replies tells them apart.
func askEveryCluster(resolvers []clusterNameResolver, name string) (found, answered bool) {
	if len(resolvers) == 0 {
		return false, false
	}
	type reply struct{ found, ok bool }
	replies := make(chan reply, len(resolvers))
	for _, cr := range resolvers {
		go func(cr clusterNameResolver) {
			f, ok := cr.ResolveInCluster(name)
			replies <- reply{f, ok}
		}(cr)
	}
	for range resolvers {
		r := <-replies
		if !r.ok {
			continue
		}
		answered = true
		if r.found {
			// The remaining goroutines finish into a buffered channel and are
			// collected; nothing is left blocked behind an answer nobody wants.
			return true, true
		}
	}
	return false, answered
}
