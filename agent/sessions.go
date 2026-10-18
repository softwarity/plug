package agent

// Sessions: one `plug` run as the agent sees it, and the one way a host ends it.
//
// A run opens several SSH connections, not one: the datapath where the run holds
// it, the transport that serves its -s names, one for its mounts, one more for a
// --env-of. Each of them presents the run's identity right after the handshake
// (sessionHelloRequest), so the agent can tell "alice's plug -s checkout" from
// "alice's laptop", and a session opens with its first connection and closes
// with its last. A connection that presents nothing (an older client, the
// datapath a macOS or Windows machine shares between its runs) belongs to no
// session: it is still tracked, and still closed when its key is.
//
// Ending a session is a statement, not a dropped line. A connection that
// simply goes away is a network blip to the client, which reconnects and puts
// its names back, so a host that only cut connections would see the session
// return within seconds. The agent first says why (sessionClosedRequest), the
// client stops its command and does not come back, and the session's id is
// remembered for a while (closedSessionMemory) so that a client which missed the
// message, reconnecting, is told the same thing instead of being let back in.
//
// What closing is NOT: a ban. A client can always start a new run, with a new
// id. Keeping somebody out is the key's business (Host.Verify), which is why
// CloseKey exists beside CloseSession: a host withdrawing a key ends what that
// key has open, in the same words.

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The three requests of the session protocol, outside any channel. Named in the
// domain the SSH spec reserves for local extensions: an agent that does not
// know them answers false, which every client reads as "no session here".
const (
	// client -> agent, right after the handshake: {ID, Version}. A refusal that
	// carries a reason means the session was closed: the client stops.
	sessionHelloRequest = "plug-session@softwarity.io"
	// client -> agent, as a connection is closed on purpose: the difference
	// between a Ctrl-C and a crash, which the TCP close alone does not tell.
	sessionByeRequest = "plug-bye@softwarity.io"
	// agent -> client: {Reason}. The client stops its command and does not
	// reconnect.
	sessionClosedRequest = "plug-closed@softwarity.io"
)

const (
	// sessionLinger is how long a session whose last connection dropped without
	// a goodbye stays open, waiting for it to come back. The client re-dials as
	// soon as its keepalive gives up (about thirty seconds), so a laptop changing
	// networks keeps one session instead of reading as two.
	sessionLinger = 90 * time.Second
	// closedSessionMemory is how long a closed session's id is refused. Long
	// against a re-dial, short enough that the list cannot grow with a host that
	// closes sessions all day.
	closedSessionMemory = 5 * time.Minute
	// closedNoticeWait bounds the wait for the client to acknowledge the notice
	// before the connection is cut regardless: a client that stopped answering
	// must not keep the names it holds.
	closedNoticeWait = 2 * time.Second
)

// SessionHost is what a Host implements to follow sessions. Optional: a Host
// without it gets the agent it always had, and the agent asks for it by type
// assertion, as it does for nothing else, so the three methods of Host stay the
// ones every embedder must write.
//
// Both callbacks are information, never authorisation, delivered in order on
// the same queue as Served and Unserved: a session's Served events come after
// its SessionOpened, and the Unserved of whatever it still held before its
// SessionClosed.
type SessionHost interface {
	SessionOpened(SessionEvent)
	SessionClosed(SessionEvent)
}

// SessionEnd says how a session ended.
type SessionEnd string

const (
	// SessionEnded: the client said goodbye. Ctrl-C, or the command finished.
	SessionEnded SessionEnd = "ended"
	// SessionDropped: the last connection went without a goodbye and did not
	// come back within sessionLinger. A process killed, a laptop that never
	// woke up, a network gone. What it had parked is restored by the agent's
	// sweep.
	SessionDropped SessionEnd = "dropped"
	// SessionClosedByHost: the host ended it (CloseSession, CloseKey), or a member of
	// an agent that admits by approval revoked its key. Reason says which, in
	// the words the client was shown.
	SessionClosedByHost SessionEnd = "closed"
	// SessionShutdown: the agent itself stopped. The client reconnects to the
	// next one, as a new session.
	SessionShutdown SessionEnd = "shutdown"
)

// SessionEvent is one session. The same value is given at the opening and at
// the end, with End, Reason and Ended filled in for the latter.
type SessionEvent struct {
	ID      string    // chosen by the client for the run, the same on all its connections
	Who     string    // the identity Host.Verify gave the key; empty when it names no one
	Key     string    // the key's SHA256 fingerprint, as `plug pubkey` and OpenSSH print it
	From    string    // the address the first connection came from
	Version string    // the client's plug version, as it said it
	Since   time.Time // when the first connection presented the session
	Ended   time.Time
	End     SessionEnd
	Reason  string // for SessionClosedByHost: what the client was told
}

// Control is the host's hand on a running agent. Made by NewControl, given to
// Start in Config.Control; before Start has run, and after it returned, every
// method finds nothing.
type Control struct {
	mu  sync.Mutex
	srv *sshServer
}

// NewControl returns a Control to pass in Config.
func NewControl() *Control { return &Control{} }

func (c *Control) attach(s *sshServer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.srv = s
	c.mu.Unlock()
}

func (c *Control) server() *sshServer {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv
}

// CloseSession ends one session: every connection it has open is told reason,
// then cut, and the names it served are restored at once. reason is shown to
// the developer as it is given ("closed by the gateway's administrator"): one
// line, 200 characters at most, the rest is dropped. Reports whether the
// session was open here.
//
// The id is refused for closedSessionMemory even when this agent never saw it.
// A gateway running several replicas, each with its agent, has a run's
// connections spread over them, and closes the session by asking every one:
// a replica the run had not reached yet must still turn it away when the
// client, having missed the notice, reconnects there.
func (c *Control) CloseSession(id, reason string) bool {
	s := c.server()
	if s == nil {
		return false
	}
	n := s.closeWhere(func(st *connState, sess *session) bool { return sess != nil && sess.ev.ID == id }, reason)
	if sessionIDRe.MatchString(id) {
		s.mu.Lock()
		if _, known := s.closedTomb(id); !known {
			s.bury(id, "", closeReason(reason))
		}
		s.mu.Unlock()
	}
	return n > 0
}

// CloseKey ends every session and connection a key has open, with the same
// notice: what a host does once it no longer accepts the key (removed, or its
// owner's right withdrawn), so the developer learns it now and in those words
// rather than at their next connection. Returns how many sessions and
// connections without one it ended.
func (c *Control) CloseKey(fingerprint, reason string) int {
	s := c.server()
	if s == nil {
		return 0
	}
	return s.closeWhere(func(st *connState, sess *session) bool {
		if sess != nil {
			return sess.ev.Key == fingerprint
		}
		return st != nil && st.key == fingerprint
	}, reason)
}

// Sessions lists the sessions open now, the ones waiting for their client to
// come back included: what a page listing live sessions shows when it opens,
// before the events take over.
func (c *Control) Sessions() []SessionEvent {
	s := c.server()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionEvent, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess.ev)
	}
	return out
}

// connState is what the agent knows of one tunnel connection beyond what SSH
// gave it.
type connState struct {
	key  string // fingerprint of the key it authenticated with
	who  string
	from string
	// session is the id it presented, "" for a connection that belongs to none.
	session string
	// bye: the client said goodbye before closing.
	bye bool
	// closed is the reason it was ended by the host, "" otherwise.
	closed string
}

// session is one run: its event, and how many connections it has open.
type session struct {
	ev     SessionEvent
	conns  int
	linger *time.Timer
}

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// helloPayload is what a client presents, and what a closed session's refusal
// carries back in its place.
type helloPayload struct {
	ID      string
	Version string
}

type closedPayload struct {
	Reason string
}

// oneLine reduces a text meant for a terminal to one printable line of at most
// n characters: a host's reason and a client's version are both printed by
// somebody else's code.
func oneLine(s string, n int) string {
	var b strings.Builder
	for _, c := range s {
		if c == '\n' || c == '\r' || c == '\t' {
			c = ' '
		}
		if c < ' ' || c == 0x7f {
			continue
		}
		b.WriteRune(c)
		if b.Len() >= n {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// hello attaches a connection to the session it presents, opening the session
// when it is the first. The answer is the reply: true, or false with the reason
// the session was closed, after which the connection is cut.
func (s *sshServer) hello(conn *ssh.ServerConn, st *connState, r *ssh.Request) {
	var p helloPayload
	if err := ssh.Unmarshal(r.Payload, &p); err != nil || !sessionIDRe.MatchString(p.ID) {
		_ = r.Reply(false, nil)
		return
	}
	s.mu.Lock()
	if t, closed := s.closedTomb(p.ID); closed {
		s.mu.Unlock()
		if t.key != "" && t.key != st.key {
			_ = r.Reply(false, nil)
			return
		}
		_ = r.Reply(false, ssh.Marshal(closedPayload{t.reason}))
		s.note("ssh: session %s from %s was closed (%s), refused its return", p.ID, conn.RemoteAddr(), t.reason)
		_ = conn.Close()
		return
	}
	if st.session != "" {
		s.mu.Unlock()
		_ = r.Reply(true, nil)
		return
	}
	sess := s.sessions[p.ID]
	if sess != nil && sess.ev.Key != st.key {
		// Another key presenting a live session's id: it neither joins it nor
		// learns anything. Answered as an agent without sessions would.
		s.mu.Unlock()
		_ = r.Reply(false, nil)
		return
	}
	opened := false
	if sess == nil {
		sess = &session{ev: SessionEvent{
			ID:      p.ID,
			Who:     st.who,
			Key:     st.key,
			From:    st.from,
			Version: oneLine(p.Version, 64),
			Since:   time.Now(),
		}}
		if s.sessions == nil {
			s.sessions = map[string]*session{}
		}
		s.sessions[p.ID] = sess
		opened = true
	} else if sess.linger != nil {
		// Back within the linger: the same session, nothing to announce.
		sess.linger.Stop()
		sess.linger = nil
	}
	sess.conns++
	st.session = p.ID
	ev := sess.ev
	s.mu.Unlock()
	_ = r.Reply(true, nil)
	if opened {
		if sh, ok := s.host.(SessionHost); ok {
			s.post(func() { sh.SessionOpened(ev) })
		}
	}
}

// closedTomb reports whether id was closed recently, and how. s.mu held.
func (s *sshServer) closedTomb(id string) (tombstone, bool) {
	s.pruneTombs()
	t, ok := s.tombs[id]
	return t, ok
}

// bury records a closed id. s.mu held.
func (s *sshServer) bury(id, key, reason string) {
	s.pruneTombs()
	if s.tombs == nil {
		s.tombs = map[string]tombstone{}
	}
	if t, ok := s.tombs[id]; ok && t.key != "" && key == "" {
		key = t.key // a broadcast close does not forget whose it was
	}
	s.tombs[id] = tombstone{reason: reason, key: key, until: time.Now().Add(closedSessionMemory)}
}

func (s *sshServer) pruneTombs() {
	now := time.Now()
	for k, t := range s.tombs {
		if now.After(t.until) {
			delete(s.tombs, k)
		}
	}
}

// tombstone is a closed session's id: why, until when, and the key it was
// open with, when this agent knew it. A closed id is refused with its reason
// to that key only; another key presenting it learns nothing.
type tombstone struct {
	reason string
	key    string
	until  time.Time
}

// leave detaches a connection that ended from its session. The session ends
// with its last connection: at once when the client said goodbye or the host
// closed it, after sessionLinger otherwise, unless a connection presenting it
// comes back first.
func (s *sshServer) leave(st *connState) {
	s.mu.Lock()
	sess := s.sessions[st.session]
	if st.session == "" || sess == nil {
		s.mu.Unlock()
		return
	}
	if sess.conns--; sess.conns > 0 {
		s.mu.Unlock()
		return
	}
	switch {
	case st.closed != "":
		s.finish(sess, SessionClosedByHost, st.closed)
	case st.bye:
		s.finish(sess, SessionEnded, "")
	case s.stopping:
		s.finish(sess, SessionShutdown, "")
	default:
		id := sess.ev.ID
		sess.linger = time.AfterFunc(s.lingerFor(), func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if cur := s.sessions[id]; cur == sess && sess.conns == 0 && sess.linger != nil {
				s.finish(sess, SessionDropped, "")
			}
		})
	}
	s.mu.Unlock()
}

func (s *sshServer) lingerFor() time.Duration {
	if s.linger > 0 {
		return s.linger
	}
	return sessionLinger
}

// finish ends a session and tells the host. s.mu held.
func (s *sshServer) finish(sess *session, end SessionEnd, reason string) {
	if sess.linger != nil {
		sess.linger.Stop()
		sess.linger = nil
	}
	delete(s.sessions, sess.ev.ID)
	ev := sess.ev
	ev.End, ev.Reason, ev.Ended = end, reason, time.Now()
	if sh, ok := s.host.(SessionHost); ok {
		s.post(func() { sh.SessionClosed(ev) })
	}
}

// closeWhere ends every connection, and every lingering session, that match
// says so, telling each client reason first. Returns how many sessions and
// session-less connections it ended.
func (s *sshServer) closeWhere(match func(*connState, *session) bool, reason string) int {
	reason = closeReason(reason)
	s.mu.Lock()
	type victim struct {
		conn *ssh.ServerConn
		st   *connState
	}
	var victims []victim
	ended := map[string]string{} // id -> key
	loose := 0
	for c, st := range s.conns {
		if st == nil {
			continue
		}
		sess := s.sessions[st.session]
		if st.session == "" {
			sess = nil
		}
		if !match(st, sess) || st.closed != "" {
			continue
		}
		st.closed = reason
		victims = append(victims, victim{c, st})
		if sess != nil {
			ended[sess.ev.ID] = sess.ev.Key
		} else {
			loose++
		}
	}
	// A session waiting for its client to come back has no connection to
	// tell: it ends now, and its id is refused when the client returns.
	for id, sess := range s.sessions {
		if sess.conns == 0 && match(nil, sess) {
			ended[id] = sess.ev.Key
			s.finish(sess, SessionClosedByHost, reason)
		}
	}
	for id, key := range ended {
		s.bury(id, key, reason)
	}
	s.mu.Unlock()
	for _, v := range victims {
		s.note("ssh: closing %s from %s (%s)", firstNonEmpty(v.st.session, "a connection"), v.conn.RemoteAddr(), reason)
		go s.tellClosed(v.conn, reason)
	}
	return len(ended) + loose
}

// closeReason is reason as the developer reads it.
func closeReason(reason string) string {
	if r := oneLine(reason, 200); r != "" {
		return r
	}
	return "closed by the cluster"
}

// tellClosed says why, waits a moment for the client to have heard it, and
// cuts the connection. The connection's own teardown then withdraws its names,
// ends its session and restores what it parked (handle).
func (s *sshServer) tellClosed(conn *ssh.ServerConn, reason string) {
	defer s.absorb("closing a session", nil)()
	done := make(chan struct{})
	go func() {
		_, _, _ = conn.SendRequest(sessionClosedRequest, true, ssh.Marshal(closedPayload{reason}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closedNoticeWait):
	}
	_ = conn.Close()
}

// restoreAfterClose runs the sweep once a closed connection has released its
// forwards: what the session parked comes back now rather than at the next
// periodic pass, a minute later.
func (s *sshServer) restoreAfterClose() {
	if s.sweep == nil {
		return
	}
	go runQuietly("the sweep after a closed session", s.logf, s.sweep)
}
