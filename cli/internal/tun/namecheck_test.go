package tun

import (
	"net"
	"sync"
	"testing"
	"time"
)

// fakeResolver is a Dialer whose agent answers the resolve verb from a map.
type fakeResolver struct {
	names map[string]bool
	ok    bool
	calls int
}

func (f *fakeResolver) DialCluster(string) (net.Conn, error) { return nil, nil }
func (f *fakeResolver) ResolveInCluster(name string) (bool, bool) {
	f.calls++
	return f.names[name], f.ok
}

// plainDialer has no ResolveInCluster at all (an exotic Dialer impl).
type plainDialer struct{}

func (plainDialer) DialCluster(string) (net.Conn, error) { return nil, nil }

func TestNameCheckerVerdictsAndCache(t *testing.T) {
	fr := &fakeResolver{names: map[string]bool{"httpbin": true}, ok: true}
	check := newNameChecker(func() []Dialer { return []Dialer{fr} }, logfn(func(string, ...any) {}))

	if !check("httpbin") {
		t.Fatal("existing name must mint")
	}
	if check("absent") {
		t.Fatal("absent name must NXDOMAIN")
	}
	// Cached: two more lookups, zero extra round-trips.
	before := fr.calls
	check("httpbin")
	check("absent")
	if fr.calls != before {
		t.Fatalf("cache miss: %d extra agent calls", fr.calls-before)
	}
}

func TestNameCheckerFallsBackToMinting(t *testing.T) {
	log := logfn(func(string, ...any) {})
	// Old agent (ok=false): mint as before.
	old := &fakeResolver{names: map[string]bool{}, ok: false}
	if !newNameChecker(func() []Dialer { return []Dialer{old} }, log)("whatever") {
		t.Fatal("an old agent must fall back to minting")
	}
	// No transports at all: mint.
	if !newNameChecker(func() []Dialer { return nil }, log)("whatever") {
		t.Fatal("no transports must fall back to minting")
	}
	// A Dialer without the facet: mint.
	if !newNameChecker(func() []Dialer { return []Dialer{plainDialer{}} }, log)("whatever") {
		t.Fatal("a facet-less dialer must fall back to minting")
	}
}

func TestNameCheckerAnyClusterWins(t *testing.T) {
	a := &fakeResolver{names: map[string]bool{}, ok: true}
	b := &fakeResolver{names: map[string]bool{"svc": true}, ok: true}
	check := newNameChecker(func() []Dialer { return []Dialer{a, b} }, logfn(func(string, ...any) {}))
	if !check("svc") {
		t.Fatal("a name present in ANY connected cluster must mint")
	}
}

// slowResolver answers after a delay, and counts how many times it was asked.
type slowResolver struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (s *slowResolver) DialCluster(string) (net.Conn, error) { return nil, nil }
func (s *slowResolver) ResolveInCluster(string) (bool, bool) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return false, true // answered: the name is absent
}
func (s *slowResolver) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

// The Windows shape, and the reason this cost a CI leg: one name is asked about
// SEVERAL TIMES AT ONCE. The search suffix turns `svc` into a query for
// `svc.plug` and one for `svc` — both land on this key — and the resolver
// re-sends after about a second while nothing has answered yet.
//
// The cache cannot help there: it only holds an answer once one is back. So
// each question used to open its own session and wait out the agent's budget,
// which an ABSENT name burns in full by definition — the agent cannot say "no"
// before it has finished looking. Stacked up, they outlasted what a client
// waits: a leg gave up resolving after 8s on a name plug decides in under two.
func TestConcurrentQuestionsAboutOneNameCostOneRoundTrip(t *testing.T) {
	slow := &slowResolver{delay: 200 * time.Millisecond}
	check := newNameChecker(func() []Dialer { return []Dialer{slow} }, logfn(func(string, ...any) {}))

	const askers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	minted := make([]bool, askers)
	for i := 0; i < askers; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; minted[i] = check("absent") }(i)
	}
	began := time.Now()
	close(start)
	wg.Wait()
	took := time.Since(began)

	if n := slow.count(); n != 1 {
		t.Fatalf("one name asked %d times at once cost %d agent round trips — want 1", askers, n)
	}
	// Serialised, eight of these would take 1.6s. The bound is deliberately
	// loose: what is asserted is that they did not queue, not a stopwatch.
	if took > time.Second {
		t.Fatalf("the questions queued: %s for %d concurrent lookups of one name", took.Round(time.Millisecond), askers)
	}
	for i, m := range minted {
		if m {
			t.Fatalf("asker %d minted a name the agent said was absent — a waiter got the wrong verdict", i)
		}
	}
}

func TestAnswerDNSHonestNXDOMAIN(t *testing.T) {
	tab := newFaketab(fakeBase)
	deny := nameChecker(func(string) bool { return false })
	resp := answerDNS(query("ghost", 1), tab, newUpstream(nil), deny)
	if resp == nil {
		t.Fatal("no response")
	}
	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("want NXDOMAIN (3), got rcode %d", rcode)
	}
	if _, ok := tab.lookup(fakeBase | 1); ok {
		t.Fatal("a denied name must not be minted")
	}
}

// The poisoning this guards against, reproduced live on a real cluster: a
// session is killed, its name is gone from the cluster, and the stub kept
// answering "found" from a FIVE-MINUTE cache — minting a fake address that, on
// a plugged workstation running Docker Desktop, was echoed back INTO the
// cluster (the embedded DNS forwards absent names upstream, which is us). A
// gateway inside the cluster then cached an address that only exists on this
// machine, and stayed broken until restarted.
//
// The product property is: plug's answers are honest within checkTTL, in BOTH
// directions. This test pays real seconds for it on purpose — it is the direct
// regression test for the incident, against the shipped constant.
func TestAVerdictNeverOutlivesCheckTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps past checkTTL")
	}
	fr := &fakeResolver{names: map[string]bool{"fpl-svc": true}, ok: true}
	check := newNameChecker(func() []Dialer { return []Dialer{fr} }, logfn(func(string, ...any) {}))

	if !check("fpl-svc") {
		t.Fatal("served name must mint")
	}
	// The Ctrl-C: the name vanishes from the cluster.
	fr.names["fpl-svc"] = false

	time.Sleep(checkTTL + 500*time.Millisecond)
	if check("fpl-svc") {
		t.Fatal("the stub still said a killed session's name existed after checkTTL — this is the gateway-poisoning bug")
	}

	// And the other direction: an absent name that gets SERVED must appear
	// within the same bound, not linger as NXDOMAIN.
	if check("brand-new") {
		t.Fatal("absent name must NXDOMAIN")
	}
	fr.names["brand-new"] = true
	time.Sleep(checkTTL + 500*time.Millisecond)
	if !check("brand-new") {
		t.Fatal("a freshly served name stayed NXDOMAIN past checkTTL")
	}
}
