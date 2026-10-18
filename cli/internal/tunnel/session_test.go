package tunnel

// A run's transport presents its session and stops for good when the agent
// ends it. The agent here speaks the session protocol and nothing else, the
// way agent/sessions.go does: a hello it records or refuses, a notice it sends.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type sessionAgent struct {
	addr  string
	conns atomic.Int32

	mu     sync.Mutex
	hellos []string          // session ids presented, in order
	byes   int               // goodbyes heard
	live   []*ssh.ServerConn // connections still open
	refuse string            // when set, a hello is refused with this reason
	silent bool              // an agent that does not know sessions
	told   chan struct{}     // closed once the client acknowledged a notice
}

func newSessionAgent(t *testing.T) *sessionAgent {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	a := &sessionAgent{addr: ln.Addr().String(), told: make(chan struct{})}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			a.conns.Add(1)
			go a.serve(c, cfg)
		}
	}()
	return a
}

func (a *sessionAgent) serve(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	a.mu.Lock()
	a.live = append(a.live, sc)
	a.mu.Unlock()
	go func() {
		for nc := range chans {
			nc.Reject(ssh.Prohibited, "sessions only")
		}
	}()
	for r := range reqs {
		a.mu.Lock()
		switch {
		case r.Type == helloRequest && !a.silent:
			var p struct{ ID, Version string }
			_ = ssh.Unmarshal(r.Payload, &p)
			a.hellos = append(a.hellos, p.ID)
			if a.refuse != "" {
				reason := a.refuse
				a.mu.Unlock()
				_ = r.Reply(false, ssh.Marshal(struct{ Reason string }{reason}))
				sc.Close()
				continue
			}
			_ = r.Reply(true, nil)
		case r.Type == byeRequest:
			a.byes++
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
		a.mu.Unlock()
	}
}

// close ends every live connection the way the agent does: the notice first,
// then the cut.
func (a *sessionAgent) close(reason string) {
	a.mu.Lock()
	live := a.live
	a.live = nil
	a.refuse = reason
	a.mu.Unlock()
	for _, sc := range live {
		ok, _, _ := sc.SendRequest(closedRequest, true, ssh.Marshal(struct{ Reason string }{reason}))
		if ok {
			select {
			case <-a.told:
			default:
				close(a.told)
			}
		}
		sc.Close()
	}
}

func (a *sessionAgent) snapshot() ([]string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.hellos...), a.byes
}

func dialRun(t *testing.T, a *sessionAgent, id string) *Transport {
	t.Helper()
	host, port, _ := net.SplitHostPort(a.addr)
	tr, err := DialSession(host, port, "plug", [][]byte{clientKeyPEM(t)}, "", nil, Session{ID: id, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func waitClosed(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the holder was never told the session was closed")
		return nil
	}
}

// Every connection presents the run; closing on purpose says goodbye first.
func TestATransportPresentsItsRunAndSaysGoodbye(t *testing.T) {
	a := newSessionAgent(t)
	tr := dialRun(t, a, "0123456789abcdef")
	if hellos, _ := a.snapshot(); len(hellos) != 1 || hellos[0] != "0123456789abcdef" {
		t.Fatalf("the agent was presented %v", hellos)
	}
	tr.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, byes := a.snapshot(); byes == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("closing the transport did not say goodbye")
}

// Told the session is closed, the transport tells its holder in the agent's
// words and never dials again: a dropped line would have come back.
func TestAClosedSessionIsFinal(t *testing.T) {
	a := newSessionAgent(t)
	tr := dialRun(t, a, "0123456789abcdef")
	got := make(chan error, 1)
	tr.OnSessionClosed(func(err error) { got <- err })

	a.close("closed by the gateway's administrator")
	err := waitClosed(t, got)
	var ce *ClosedError
	if !errors.As(err, &ce) || ce.Reason != "closed by the gateway's administrator" {
		t.Fatalf("told %v", err)
	}
	if tr.SessionClosed() == nil || tr.Refused() == nil {
		t.Error("the transport does not remember it was closed")
	}
	before := a.conns.Load()
	_, derr := tr.DialCluster("db:5432")
	if !errors.As(derr, &ce) {
		t.Errorf("a dial after the close answered %v", derr)
	}
	if after := a.conns.Load(); after != before {
		t.Errorf("the transport dialled the agent again (%d connections, was %d)", after, before)
	}
	late := make(chan error, 1)
	tr.OnSessionClosed(func(err error) { late <- err })
	waitClosed(t, late)
}

// A transport that missed the notice, reconnecting, is refused with the same
// words and stops there.
func TestAMissedNoticeIsLearntOnTheWayBack(t *testing.T) {
	a := newSessionAgent(t)
	tr := dialRun(t, a, "0123456789abcdef")
	got := make(chan error, 1)
	tr.OnSessionClosed(func(err error) { got <- err })

	a.mu.Lock()
	live := a.live
	a.live = nil
	a.refuse = "your key was removed"
	a.mu.Unlock()
	for _, sc := range live {
		sc.Close() // cut without a word, as a lost notice looks
	}
	_, derr := tr.DialCluster("db:5432")
	if derr == nil {
		t.Fatal("a dial on a cut connection succeeded")
	}
	err := waitClosed(t, got)
	var ce *ClosedError
	if !errors.As(err, &ce) || ce.Reason != "your key was removed" {
		t.Fatalf("told %v", err)
	}
}

// An agent that knows nothing of sessions answers the hello false, with
// nothing: that is not a refusal, and the run goes on.
func TestAnAgentWithoutSessionsLetsTheRunIn(t *testing.T) {
	a := newSessionAgent(t)
	a.mu.Lock()
	a.silent = true
	a.mu.Unlock()
	tr := dialRun(t, a, "0123456789abcdef")
	if tr.SessionClosed() != nil {
		t.Fatal("an agent without sessions read as having closed one")
	}
}
