package agent

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A query the network lost never answers: the lookup that sent it waits out
// the whole budget. The lookup asked a moment later is the one that answers,
// and it must win, or a name the cluster has reads as absent.
func TestALostQueryIsAskedAgainWithinTheBudget(t *testing.T) {
	var calls atomic.Int32
	lookup := func(ctx context.Context, name string) ([]string, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done() // the dropped packet
			return nil, ctx.Err()
		}
		return []string{"10.96.0.42"}, nil
	}
	start := time.Now()
	addrs, err := lookupHedged(lookup, "adm-echo", 800*time.Millisecond, 50*time.Millisecond)
	if err != nil || len(addrs) != 1 || addrs[0] != "10.96.0.42" {
		t.Fatalf("got %v, %v; want the second lookup's address", addrs, err)
	}
	if took := time.Since(start); took >= 800*time.Millisecond {
		t.Fatalf("answered in %s, the whole budget: the second lookup was not asked", took)
	}
}

// The resolver saying the name does not exist is an answer too, and ends it.
func TestTheResolversNoEndsTheLookup(t *testing.T) {
	lookup := func(ctx context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	start := time.Now()
	_, err := lookupHedged(lookup, "nope", 800*time.Millisecond, 50*time.Millisecond)
	var dnsErr *net.DNSError
	if err == nil || !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatalf("got %v, want the resolver's not-found", err)
	}
	if took := time.Since(start); took >= 400*time.Millisecond {
		t.Fatalf("took %s: a definite no waited for the budget", took)
	}
}

// Nothing ever answers: the caller sees a timeout once the budget is spent,
// not later, and decides with its witness as before.
func TestNoAnswerAtAllIsATimeoutAtTheBudget(t *testing.T) {
	lookup := func(ctx context.Context, name string) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	_, err := lookupHedged(lookup, "slow", 300*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatal("no lookup answered, yet no error")
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("took %s against a 300ms budget", took)
	}
}
