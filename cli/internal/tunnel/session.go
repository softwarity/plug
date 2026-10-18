package tunnel

// The session a run presents to the agent, and the notice that ends it.
//
// A run opens several connections to the agent (the datapath where it holds
// one, the -s transport, the mounts', a --env-of's); each says which run it
// belongs to right after the handshake, so the agent, and a gateway behind it,
// see one session per run. The agent can then end that session on purpose: a
// gateway's administrator closing it, a key withdrawn, a member revoked. It
// says so first (closedRequest), because a connection that merely drops is a
// network blip to this package, which reconnects and re-arms everything: a
// session ended without a word would be back within seconds.
//
// Once told, a transport never dials again, and its holder hears it once
// (OnSessionClosed). The agent also remembers the session for a few minutes and
// refuses its hello with the same reason, so a transport that missed the
// notice, reconnecting, learns it there instead of being let back in.
//
// The agent's side is agent/sessions.go; the request names are shared.

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	helloRequest  = "plug-session@softwarity.io"
	byeRequest    = "plug-bye@softwarity.io"
	closedRequest = "plug-closed@softwarity.io"
)

// helloTimeout bounds the hello: an agent too old to know it answers false at
// once, so waiting longer than a round-trip only ever waits on a dead path.
const helloTimeout = 10 * time.Second

// Session is the run a transport belongs to. The zero value is no session: the
// connection presents nothing, as the shared datapath daemon's do.
type Session struct {
	ID      string // the same on every connection of the run
	Version string // plug's version, which the gateway lists beside the session
}

// ClosedError is the agent ending this session on purpose, with its reason. It
// is final: the transport does not reconnect, whatever the error path that
// reports it.
type ClosedError struct {
	Addr   string
	Reason string
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("%s closed this session: %s", e.Addr, e.Reason)
}

// SessionClosed reports the agent ending this transport's session, nil while it
// has not.
func (t *Transport) SessionClosed() error {
	if ce := t.closedBy.Load(); ce != nil {
		return ce
	}
	return nil
}

// OnSessionClosed registers f, called once, in its own goroutine, when the
// agent ends this transport's session. Registered after the fact, f is called
// at once.
func (t *Transport) OnSessionClosed(f func(error)) {
	t.mu.Lock()
	if ce := t.closedBy.Load(); ce != nil {
		t.mu.Unlock()
		go f(ce)
		return
	}
	t.onClosed = append(t.onClosed, f)
	t.mu.Unlock()
}

// markClosed records the agent's notice, once, and tells whoever registered.
func (t *Transport) markClosed(reason string) *ClosedError {
	ce := &ClosedError{Addr: t.host + ":" + t.port, Reason: reason}
	t.mu.Lock()
	if !t.closedBy.CompareAndSwap(nil, ce) {
		ce = t.closedBy.Load()
		t.mu.Unlock()
		return ce
	}
	fs := t.onClosed
	t.onClosed = nil
	t.mu.Unlock()
	for _, f := range fs {
		go f(ce)
	}
	return ce
}

// serverRequest takes the agent's own global requests off the connection
// before x/crypto, which refuses every one of them. Reports whether it took r.
func (t *Transport) serverRequest(r *ssh.Request) bool {
	if r.Type != closedRequest {
		return false
	}
	var p struct{ Reason string }
	_ = ssh.Unmarshal(r.Payload, &p)
	if r.WantReply {
		_ = r.Reply(true, nil)
	}
	t.markClosed(oneLine(p.Reason))
	return true
}

// filterRequests hands handle every global request the agent sends, and passes
// on to x/crypto only the ones it did not take.
func filterRequests(in <-chan *ssh.Request, handle func(*ssh.Request) bool) <-chan *ssh.Request {
	out := make(chan *ssh.Request)
	go func() {
		defer close(out)
		for r := range in {
			if !handle(r) {
				out <- r
			}
		}
	}()
	return out
}

// hello presents the session on a fresh client. It returns the reason when the
// agent refuses it as closed; an agent that does not know sessions answers
// false with nothing, which is not a refusal.
func hello(cl *ssh.Client, s Session, timeout time.Duration) (closed string, err error) {
	type answer struct {
		ok      bool
		payload []byte
		err     error
	}
	res := make(chan answer, 1)
	go func() {
		ok, payload, err := cl.SendRequest(helloRequest, true, ssh.Marshal(struct{ ID, Version string }{s.ID, s.Version}))
		res <- answer{ok, payload, err}
	}()
	select {
	case a := <-res:
		if a.err != nil {
			return "", a.err
		}
		if a.ok || len(a.payload) == 0 {
			return "", nil
		}
		var p struct{ Reason string }
		if ssh.Unmarshal(a.payload, &p) != nil || p.Reason == "" {
			return "", nil
		}
		return oneLine(p.Reason), nil
	case <-time.After(timeout):
		return "", fmt.Errorf("the agent did not answer the session hello within %s", timeout)
	}
}

// sayBye tells the agent this connection is closed on purpose, so the session
// reads as ended rather than dropped. Answered, so the close that follows does
// not overtake it. Bounded: a dead connection is about to be closed anyway,
// and must not hold the close.
func sayBye(cl *ssh.Client) {
	done := make(chan struct{})
	go func() {
		_, _, _ = cl.SendRequest(byeRequest, true, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// oneLine keeps an agent's words printable on one line: they reach a terminal.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < ' ' || r == 0x7f:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	if s == "" {
		s = "closed by the cluster"
	}
	return s
}
