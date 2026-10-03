package tun

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The maps fed by every process on the machine must not grow for ever: a
// daemon runs for weeks, and each of these used to keep one entry per name it
// had ever been asked about. What they remember is only worth its TTL, so at
// the bound the stale entries go.

func TestALogLimiterForgetsKeysWhoseWindowHasPassed(t *testing.T) {
	l := newLogLimiter(time.Minute)
	clock := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return clock }

	for i := 0; i < limiterMax; i++ {
		l.allow("name-" + strconv.Itoa(i))
	}
	if len(l.last) != limiterMax {
		t.Fatalf("remembered %d keys after %d first sightings, want all of them", len(l.last), limiterMax)
	}
	// One more, with every window passed: the stale keys go, the new one stays.
	clock = clock.Add(2 * time.Minute)
	if !l.allow("one-more") {
		t.Fatal("a key never seen was not allowed")
	}
	if len(l.last) != 1 {
		t.Errorf("remembered %d keys past the bound with every window elapsed, want the one just logged", len(l.last))
	}

	// A burst of fresh keys that fills the bound by itself is forgotten whole:
	// a repeat logged once more is cheaper than a map that only grows.
	for i := 0; i < limiterMax; i++ {
		l.allow("burst-" + strconv.Itoa(i))
	}
	l.allow("over")
	if len(l.last) > limiterMax {
		t.Errorf("remembered %d keys, more than the bound %d", len(l.last), limiterMax)
	}
	// And a key within its window is still held back; the bound does not
	// turn the limiter off.
	if l.allow("over") {
		t.Error("a key logged a moment ago was allowed again")
	}
}

func TestANameCacheForgetsExpiredVerdicts(t *testing.T) {
	fr := &fakeResolver{names: map[string]bool{}, ok: true}
	c := newNameCache(func() []Dialer { return []Dialer{fr} }, logfn(func(string, ...any) {}))
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }

	for i := 0; i < verdictCacheMax; i++ {
		c.check("svc-" + strconv.Itoa(i))
	}
	if len(c.cache) != verdictCacheMax {
		t.Fatalf("cached %d verdicts after %d names, want all of them", len(c.cache), verdictCacheMax)
	}
	clock = clock.Add(2 * checkTTL)
	c.check("one-more")
	if len(c.cache) != 1 {
		t.Errorf("cached %d verdicts past the bound with every TTL elapsed, want the one just asked", len(c.cache))
	}
	// A fresh burst is forgotten whole rather than kept.
	for i := 0; i < verdictCacheMax+1; i++ {
		c.check("burst-" + strconv.Itoa(i))
	}
	if len(c.cache) > verdictCacheMax {
		t.Errorf("cached %d verdicts, more than the bound %d", len(c.cache), verdictCacheMax)
	}
	// Forgetting is a memory matter only: a verdict still within its TTL is
	// not re-asked.
	calls := fr.calls
	c.check("burst-" + strconv.Itoa(verdictCacheMax))
	if fr.calls != calls {
		t.Error("a verdict still within its TTL was asked again")
	}
}

// gatedResolver blocks every question until released, and counts how many are
// in flight at once.
type gatedResolver struct {
	release  chan struct{}
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (g *gatedResolver) DialCluster(string) (net.Conn, error) { return nil, nil }
func (g *gatedResolver) ResolveInCluster(string) (bool, bool) {
	n := g.inFlight.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-g.release
	g.inFlight.Add(-1)
	return false, true
}

// Each question is an SSH session on a cluster's tunnel, and a burst of
// unknown names used to open one per name per cluster, all at once.
func TestClusterQuestionsOnTheWireAreCapped(t *testing.T) {
	saved := askSlots
	askSlots = make(chan struct{}, 3)
	t.Cleanup(func() { askSlots = saved })

	g := &gatedResolver{release: make(chan struct{})}
	resolvers := []clusterNameResolver{g, g}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			askEveryCluster(resolvers, "name-"+strconv.Itoa(i))
		}(i)
	}
	// Twenty questions want to go out; let them pile up on the cap.
	deadline := time.Now().Add(2 * time.Second)
	for g.inFlight.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // long enough for a fourth to slip in, if the cap let it
	if p := g.peak.Load(); p != 3 {
		t.Errorf("%d questions on the wire at once with a cap of 3", p)
	}
	close(g.release)
	wg.Wait()
	if p := g.peak.Load(); p > 3 {
		t.Errorf("%d questions on the wire at once over the whole run, cap 3", p)
	}
}
