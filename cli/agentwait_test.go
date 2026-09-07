package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// A dial that succeeded proves the agent answered a HANDSHAKE. It proves nothing
// about the verb that follows, and an established SSH session has no deadline of
// its own: its channel is a stream over a mux with no SetReadDeadline. So an
// agent that connects and then never answers `version` used to hold the launcher
// for ever, before plug had printed one line, and what the person saw was a
// command that never started.
//
// Closing the connection is the only thing that unblocks such a read, so the
// test asserts BOTH halves: a sentence comes back, and the closer was closed.
func TestAnAgentThatNeverAnswersEndsInASentence(t *testing.T) {
	var closed atomic.Bool
	release := make(chan struct{})
	defer close(release)

	err := bounded(closerFunc(func() error { closed.Store(true); return nil }),
		50*time.Millisecond, "`version`", func() error { <-release; return nil })

	if err == nil {
		t.Fatal("waiting for ever came back as success")
	}
	if !strings.Contains(err.Error(), "never answered `version`") {
		t.Fatalf("the message does not say what went unanswered: %v", err)
	}
	if !closed.Load() {
		t.Fatal("the connection was not closed, so the read it was blocked on is still blocked")
	}
}

// The bound must not become a second failure mode: an agent that answers, even
// with an error, must have that error surface untouched and its connection left
// alone for the caller's defer to close.
func TestAnAnswerCarriesItsOwnErrorThrough(t *testing.T) {
	var closed atomic.Bool
	want := errors.New("no such verb")

	err := bounded(closerFunc(func() error { closed.Store(true); return nil }),
		5*time.Second, "`version`", func() error { return want })

	if !errors.Is(err, want) {
		t.Fatalf("the agent's own error did not come through: %v", err)
	}
	if closed.Load() {
		t.Fatal("the connection was closed on an answer that arrived in time")
	}
}
