package tunnel

import (
	"errors"
	"testing"
	"time"
)

// fakePinger answers SendRequest after `delay`, with `err`.
type fakePinger struct {
	delay time.Duration
	err   error
}

func (f fakePinger) SendRequest(string, bool, []byte) (bool, []byte, error) {
	time.Sleep(f.delay)
	return false, nil, f.err
}

// The fix: a keepalive whose reply never comes (a zombie connection — ESTABLISHED
// but dead end to end, after a sleep / a suspended Docker VM) must count as a
// MISS, not block forever. A prompt reply is OK; an errored one is a miss.
func TestPingOK(t *testing.T) {
	if !pingOK(fakePinger{}, 100*time.Millisecond) {
		t.Fatal("a prompt, error-free reply must be OK")
	}
	if pingOK(fakePinger{err: errors.New("broken pipe")}, 100*time.Millisecond) {
		t.Fatal("an errored keepalive must be a miss")
	}
	// The zombie: the reply is slower than the timeout — must be a miss, and
	// pingOK must return about at the timeout, not wait for the hung reply.
	start := time.Now()
	if pingOK(fakePinger{delay: time.Second}, 40*time.Millisecond) {
		t.Fatal("a reply slower than the timeout must be a miss (zombie connection)")
	}
	if waited := time.Since(start); waited > 300*time.Millisecond {
		t.Fatalf("pingOK blocked %v — it must give up at the timeout, not wait for the hung reply", waited)
	}
}
