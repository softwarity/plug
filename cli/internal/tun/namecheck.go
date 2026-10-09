package tun

import (
	"strings"
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
// transport, present in ANY cluster means mint. When nobody can answer (no
// transport yet, old agents), a BARE name is minted as plug always did: a fake
// IP whose connect is refused with a log, never a hang. A DOTTED name is not:
// it is minted only on a cluster's yes. A dotted name the stub may claim
// (<service>.<namespace>, see clusterShortName) can just as well be a name of
// the local network, jira.corp or odb.lan, and minting it on nobody's word
// would take it from the whole machine for as long as no agent answers.
func newNameChecker(dialers func() []Dialer, log logfn) nameChecker {
	return newNameCache(dialers, log).check
}

// nameVerdict is one remembered answer, good until its deadline.
type nameVerdict struct {
	found bool
	until time.Time
}

// verdictCacheMax bounds the verdicts remembered. Every bare name any process
// on the machine asks about lands here (a Docker Desktop VM forwards its
// containers' unknown lookups to the host resolver, which is us), and a daemon
// that runs for weeks kept every one of them for ever. Each verdict is dead
// after checkTTL anyway, so at the bound the expired ones are forgotten; a
// burst of fresh names that fills it alone is forgotten whole, which costs one
// re-ask per name and nothing else.
const verdictCacheMax = 4096

// nameCache is the state behind a nameChecker: the verdicts, and the questions
// on the wire.
//
// inflight is a lookup already on the wire, so N concurrent questions about
// ONE name cost one round trip instead of N.
//
// The cache alone does not cover this: it only helps once an answer is
// back. Windows asks about the same name several times AT ONCE (its search
// suffix turns `svc` into a query for `svc.plug` and one for `svc`, both
// landing on this same key) and its resolver re-sends after about a second
// while nothing has answered yet. Each of those used to open its own
// session and wait out the agent's budget, and an ABSENT name costs that
// budget in full by definition: the agent cannot say "no" before it has
// finished looking. Stacked up, they outlasted what the client would wait;
// one leg gave up resolving after 8s on a name plug decides in under two.
type nameCache struct {
	dialers func() []Dialer
	log     logfn
	now     func() time.Time // time.Now, replaceable by a test

	mu       sync.Mutex
	cache    map[string]nameVerdict
	inflight map[string]*nameFlight
}

type nameFlight struct {
	done  chan struct{}
	found bool
}

func newNameCache(dialers func() []Dialer, log logfn) *nameCache {
	return &nameCache{
		dialers:  dialers,
		log:      log,
		now:      time.Now,
		cache:    map[string]nameVerdict{},
		inflight: map[string]*nameFlight{},
	}
}

// check answers "should this bare name be minted?".
func (c *nameCache) check(name string) bool {
	c.mu.Lock()
	if v, hit := c.cache[name]; hit && c.now().Before(v.until) {
		c.mu.Unlock()
		return v.found
	}
	if f, busy := c.inflight[name]; busy {
		c.mu.Unlock()
		<-f.done
		return f.found
	}
	f := &nameFlight{done: make(chan struct{})}
	c.inflight[name] = f
	c.mu.Unlock()

	started := c.now()
	var resolvers []clusterNameResolver
	for _, d := range c.dialers() {
		if cr, is := d.(clusterNameResolver); is {
			resolvers = append(resolvers, cr)
		}
	}
	found, answered := askEveryCluster(resolvers, name)
	took := c.now().Sub(started)

	// Nobody could answer (no transport yet, an agent too old): mint a bare
	// name, as plug always did, but not a dotted one (newNameChecker), and do
	// NOT cache a verdict we never got.
	result := !strings.Contains(name, ".")
	c.mu.Lock()
	delete(c.inflight, name)
	if answered {
		result = found
		if len(c.cache) >= verdictCacheMax {
			c.purge()
		}
		c.cache[name] = nameVerdict{found: found, until: c.now().Add(checkTTL)}
	}
	c.mu.Unlock()

	if answered && !found && nxLimiter.allow(name) {
		// The duration is here because it is the number that decides
		// whether a slow NXDOMAIN is the agent thinking or the link.
		c.log.f("tun: %s is in no connected cluster - NXDOMAIN in %s (repeats hidden 30s)", name, took.Round(time.Millisecond))
	}
	f.found = result
	close(f.done)
	return result
}

// purge forgets the expired verdicts, or all of them when the live ones alone
// fill the cache. Caller holds c.mu.
func (c *nameCache) purge() {
	now := c.now()
	for n, v := range c.cache {
		if !now.Before(v.until) {
			delete(c.cache, n)
		}
	}
	if len(c.cache) >= verdictCacheMax {
		c.cache = map[string]nameVerdict{}
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
//
// Bounded, though: each question is an SSH session on the cluster's tunnel, and
// every bare name any process on the machine looks up asks one of every
// cluster. A burst of unknown names (a VM forwarding its containers' lookups,
// a tool probing a wordlist) multiplied by the clusters attached is the one
// way plug can open sessions faster than the agents close them. askSlots caps
// the questions on the wire machine-wide; a question past the cap waits its
// turn rather than piling on, and the OS resolver's own timeout is what bounds
// that wait.
func askEveryCluster(resolvers []clusterNameResolver, name string) (found, answered bool) {
	if len(resolvers) == 0 {
		return false, false
	}
	type reply struct{ found, ok bool }
	replies := make(chan reply, len(resolvers))
	// The cap is read once, here: a question outlives this call when another
	// cluster answered first, and it must give its token back to the channel
	// it took it from, not to whatever askSlots names by then.
	slots := askSlots
	for _, cr := range resolvers {
		go func(cr clusterNameResolver) {
			slots <- struct{}{}
			f, ok := cr.ResolveInCluster(name)
			<-slots
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

// askSlots is the cap on cluster questions in flight, machine-wide: one token
// per question on the wire. A var so a test can shrink it and watch the cap
// hold.
var askSlots = make(chan struct{}, askMax)

// askMax: enough for a busy workstation's genuine burst (a project start-up
// resolving a dozen services against three clusters), far below what a VM's
// lookup storm would open.
const askMax = 32
