package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeVerifier answers "not reachable" until failures is exhausted, recording
// the budget each attempt was given.
type fakeVerifier struct {
	failures int
	budgets  []time.Duration
}

func (f *fakeVerifier) Verify(d time.Duration) error {
	f.budgets = append(f.budgets, d)
	if len(f.budgets) <= f.failures {
		// What a probe that is too early actually costs: the whole budget,
		// because the SYN is dropped rather than refused.
		time.Sleep(d)
		return errors.New("context deadline exceeded")
	}
	return nil
}

// shortVerifyBudget scales the retry constants down so a test spends
// milliseconds where a session spends seconds, keeping their ratios.
func shortVerifyBudget(t *testing.T) {
	t.Helper()
	budget, first, max, gap := exposeVerifyBudget, exposeVerifyFirst, exposeVerifyMax, exposeVerifyGap
	exposeVerifyBudget, exposeVerifyFirst, exposeVerifyMax, exposeVerifyGap =
		250*time.Millisecond, 8*time.Millisecond, 80*time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() {
		exposeVerifyBudget, exposeVerifyFirst, exposeVerifyMax, exposeVerifyGap = budget, first, max, gap
	})
}

// A name that comes up after a few probes must be caught by a LATER probe, not
// waited out: the regression this guards is the old one-shot-then-sleep loop,
// where the first probe alone burned the full dial timeout on every startup.
func TestVerifyExposedRetriesUntilThePathOpens(t *testing.T) {
	shortVerifyBudget(t)
	f := &fakeVerifier{failures: 3}
	start := time.Now()
	if err := verifyExposed(f, "diag", never); err != nil {
		t.Fatalf("verifyExposed: %v", err)
	}
	if len(f.budgets) != 4 {
		t.Fatalf("attempts = %d, want 4", len(f.budgets))
	}
	// Cheap probes first, then growing — an early failure must not cost what a
	// late one does.
	if f.budgets[0] != exposeVerifyFirst {
		t.Errorf("first budget = %v, want %v", f.budgets[0], exposeVerifyFirst)
	}
	for i := 1; i < len(f.budgets); i++ {
		if f.budgets[i] <= f.budgets[i-1] {
			t.Errorf("budget %d (%v) did not grow past %d (%v)", i, f.budgets[i], i-1, f.budgets[i-1])
		}
	}
	// 8+2+16+2+32+2 ≈ 62ms of probing — the point of the fix is that reaching
	// the 4th attempt costs a fraction of the overall budget, not one timeout.
	if elapsed := time.Since(start); elapsed > exposeVerifyBudget/2 {
		t.Errorf("took %v to catch a path that opened on attempt 4 — over half the %v budget",
			elapsed, exposeVerifyBudget)
	}
}

// A path that never opens must fail — bounded by the budget, and saying it kept
// trying, so the message isn't read as one unlucky probe.
func TestVerifyExposedGivesUpWithinBudget(t *testing.T) {
	shortVerifyBudget(t)
	f := &fakeVerifier{failures: 1 << 30}
	start := time.Now()
	err := verifyExposed(f, "diag", never)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("verifyExposed returned nil for a path that never opened")
	}
	if !strings.Contains(err.Error(), "still failing after") {
		t.Errorf("error does not say it retried: %v", err)
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error dropped the underlying cause: %v", err)
	}
	// Bounded: the old loop could run 4 full dial timeouts plus its sleeps.
	if elapsed > 2*exposeVerifyBudget {
		t.Errorf("took %v, want the %v budget to bound it", elapsed, exposeVerifyBudget)
	}
	if len(f.budgets) < 2 {
		t.Errorf("attempts = %d, want more than one before giving up", len(f.budgets))
	}
	for i, d := range f.budgets {
		if d > exposeVerifyMax {
			t.Errorf("attempt %d got %v, over the %v ceiling", i, d, exposeVerifyMax)
		}
	}
}

// never is the "session still running" predicate for the tests above.
func never() bool { return false }

// A check now outlives nothing: teardown must end it between probes, silently.
// Left running, it would keep narrating a name nobody is waiting on any more.
func TestVerifyExposedStopsWhenSessionEnds(t *testing.T) {
	shortVerifyBudget(t)
	f := &fakeVerifier{failures: 1 << 30}
	var stop atomic.Bool
	time.AfterFunc(20*time.Millisecond, func() { stop.Store(true) })
	start := time.Now()
	err := verifyExposed(f, "diag", stop.Load)
	if !errors.Is(err, errStopped) {
		t.Fatalf("err = %v, want errStopped", err)
	}
	// It must give up on the stop, not ride out the budget.
	if elapsed := time.Since(start); elapsed > exposeVerifyBudget/2 {
		t.Errorf("took %v — did not stop promptly", elapsed)
	}
}
