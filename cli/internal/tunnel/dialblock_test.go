package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// A dial in flight must not hold the transport hostage.
//
// The dial is the slowest thing the transport does, up to dialTimeout twice
// over, and it used to run under t.mu. Everything that merely wanted to look
// at the current client queued behind it: a DialContext whose context had long
// expired, a Close() about to discard the very connection being dialled, and
// on macOS the resolver of the whole machine, which asks every cluster whether
// a bare name exists before answering. One cluster reconnecting after a
// laptop woke up froze name resolution for every other one.
//
// The server here never completes the handshake, so the dial lasts the full
// dialTimeout; the asserted callers must come back long before that.
func TestCloseAndACancelledDialReturnWhileADialIsInFlight(t *testing.T) {
	addr := blackHoleServer(t)
	host, port, _ := net.SplitHostPort(addr)

	restore := dialTimeout
	dialTimeout = 3 * time.Second
	defer func() { dialTimeout = restore }()

	tr := &Transport{host: host, port: port, user: "plug", keys: [][]byte{clientKeyPEM(t)}, done: make(chan struct{})}
	background := make(chan error, 1)
	go func() { _, err := tr.reconnectFrom(nil); background <- err }()
	waitForDialInFlight(t, tr)

	// A caller with a context of its own gets its context back, not the dial's
	// remaining patience.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := tr.DialContext(ctx, "tcp", "svc.cluster:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialContext during a dial returned %v, want its own deadline", err)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("DialContext took %v to honour a 100ms context: it waited for the dial", took)
	}

	// Looking never waits: during a dial there is simply nothing yet.
	began = time.Now()
	if cl := tr.current(); cl != nil {
		t.Fatal("current() handed out a client while the dial was still in flight")
	}
	if la := tr.LocalAddr(); la != "" {
		t.Fatalf("LocalAddr = %q during a dial, want empty", la)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("current() and LocalAddr took %v: they queued behind the dial", took)
	}

	// Close() neither waits for the dial nor leaves its result behind.
	began = time.Now()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("Close took %v: it waited for the dial", took)
	}
	select {
	case err := <-background:
		if !errors.Is(err, errClosed) {
			t.Fatalf("the caller waiting on the dial got %v after Close, want %v", err, errClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("the caller waiting on the dial was not released by Close")
	}
	// Once the dial itself gives up, the transport is still closed and empty:
	// a connection that arrived after Close would be one nobody ends.
	deadline := time.Now().Add(2 * dialTimeout)
	for {
		tr.mu.Lock()
		inFlight := tr.dialing != nil
		tr.mu.Unlock()
		if !inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dial never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tr.current() != nil {
		t.Fatal("a dial that ended after Close installed its client")
	}
}

// Everyone who finds the transport dead during ONE dial shares it, and gets the
// same answer: a second caller must neither start a second dial nor be refused.
func TestCallersArrivingDuringADialShareIt(t *testing.T) {
	agent := newHoldingAgent(t)
	tr, _ := connectedTransport(t, agent)
	dead := tr.current()

	const racers = 6
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := tr.reconnectCtx(ctx, dead)
			results <- err
		}()
	}
	for i := 0; i < racers; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a caller sharing the dial was refused: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("a caller sharing the dial never came back")
		}
	}
	if n := agent.conns.Load(); n != 2 {
		t.Fatalf("%d callers finding one dead client opened %d connections in total, want 2", racers, n)
	}
}

// waitForDialInFlight returns once the transport has registered a dial, which
// is the moment the assertions above are about.
func waitForDialInFlight(t *testing.T, tr *Transport) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr.mu.Lock()
		inFlight := tr.dialing != nil
		tr.mu.Unlock()
		if inFlight {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no dial was ever started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
