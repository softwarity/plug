package agent

// Sessions as a host sees them, and the notice a closed one gets. Every client
// below speaks the session protocol by hand, the way plug's tunnel package
// does, so what is proven is the wire and not a helper's reading of it.

import (
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sessionHost is fakeHost that also follows sessions.
type sessionHost struct {
	fakeHost
	opened chan SessionEvent
	closed chan SessionEvent
}

func (h *sessionHost) SessionOpened(e SessionEvent) { h.opened <- e }
func (h *sessionHost) SessionClosed(e SessionEvent) { h.closed <- e }

// sessionServer runs an agent whose host accepts every key it is given, with a
// short linger, and the Control a gateway would hold.
func sessionServer(t *testing.T, keys map[string]string) (string, *sessionHost, *Control) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionHost{
		fakeHost: fakeHost{signer: hk, allowed: keys},
		opened:   make(chan SessionEvent, 8),
		closed:   make(chan SessionEvent, 8),
	}
	srv := &sshServer{
		host: h, hostKey: hk,
		logf:    func(string, ...any) {},
		execFor: func(string) []string { return []string{"/bin/sh", "-c", "printf ok"} },
		linger:  300 * time.Millisecond,
	}
	ctl := NewControl()
	ctl.attach(srv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return ln.Addr().String(), h, ctl
}

// runConn is one connection of a run: the client, and the notices the agent
// sent it.
type runConn struct {
	cl      *ssh.Client
	notices chan string
}

// connect dials as the tunnel user and presents session id, as plug does on
// every connection of a run. It returns the refusal's reason when the agent
// refuses the session.
func connect(t *testing.T, addr string, key ssh.Signer, id string) (*runConn, string) {
	t.Helper()
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, &ssh.ClientConfig{
		User:            tunnelUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	rc := &runConn{notices: make(chan string, 1)}
	mine := make(chan *ssh.Request)
	go func() {
		defer close(mine)
		for r := range reqs {
			if r.Type == sessionClosedRequest {
				var p closedPayload
				_ = ssh.Unmarshal(r.Payload, &p)
				_ = r.Reply(true, nil)
				rc.notices <- p.Reason
				continue
			}
			mine <- r
		}
	}()
	rc.cl = ssh.NewClient(sc, chans, mine)
	if id == "" {
		return rc, ""
	}
	ok, payload, err := rc.cl.SendRequest(sessionHelloRequest, true, ssh.Marshal(helloPayload{ID: id, Version: "9.9.9"}))
	if err != nil {
		return rc, "hello failed: " + err.Error()
	}
	if !ok {
		var p closedPayload
		_ = ssh.Unmarshal(payload, &p)
		return rc, p.Reason
	}
	return rc, ""
}

func (rc *runConn) bye() {
	_, _, _ = rc.cl.SendRequest(sessionByeRequest, false, nil)
	rc.cl.Close()
}

func waitEvent(t *testing.T, ch chan SessionEvent, what string) SessionEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatalf("the host was never told %s", what)
		return SessionEvent{}
	}
}

func noEvent(t *testing.T, ch chan SessionEvent, what string, d time.Duration) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("the host was told %s: %+v", what, e)
	case <-time.After(d):
	}
}

const runID = "0123456789abcdef"

// A run's connections are one session: it opens with the first, closes with
// the last, and a goodbye makes that close an end rather than a drop.
func TestARunIsOneSessionAcrossItsConnections(t *testing.T) {
	key, pub := newTestKey(t)
	fp := ssh.FingerprintSHA256(pub)
	addr, h, _ := sessionServer(t, map[string]string{fp: "alice"})

	first, refused := connect(t, addr, key, runID)
	if refused != "" {
		t.Fatalf("a new session was refused: %s", refused)
	}
	e := waitEvent(t, h.opened, "the session opened")
	if e.ID != runID || e.Who != "alice" || e.Key != fp || e.Version != "9.9.9" || e.From != "127.0.0.1" || e.Since.IsZero() {
		t.Errorf("opened %+v", e)
	}
	second, _ := connect(t, addr, key, runID)
	noEvent(t, h.opened, "a second session for the run's second connection", 150*time.Millisecond)

	first.bye()
	noEvent(t, h.closed, "the session closed while a connection was still open", 150*time.Millisecond)
	second.bye()
	e = waitEvent(t, h.closed, "the session ended")
	if e.ID != runID || e.End != SessionEnded || e.Ended.IsZero() {
		t.Errorf("closed %+v", e)
	}
}

// A connection that drops without a goodbye is a network until proven
// otherwise: the session waits for it, carries on when it comes back, and
// reads as dropped only when it does not.
func TestASessionWaitsForItsClientToComeBack(t *testing.T) {
	key, pub := newTestKey(t)
	addr, h, _ := sessionServer(t, map[string]string{ssh.FingerprintSHA256(pub): "alice"})

	rc, _ := connect(t, addr, key, runID)
	waitEvent(t, h.opened, "the session opened")
	rc.cl.Close()
	back, refused := connect(t, addr, key, runID)
	if refused != "" {
		t.Fatalf("a session coming back was refused: %s", refused)
	}
	noEvent(t, h.opened, "a new session for a client that came back", 100*time.Millisecond)
	noEvent(t, h.closed, "the session closed although its client came back", 400*time.Millisecond)

	back.cl.Close()
	if e := waitEvent(t, h.closed, "the session dropped"); e.End != SessionDropped {
		t.Errorf("End = %q, want %q", e.End, SessionDropped)
	}
}

// The host closes a session: the client is told why, its connection ends, the
// host hears the end with that reason, and the same run coming back is refused
// with the same words rather than let back in.
func TestAClosedSessionIsToldWhyAndStaysClosed(t *testing.T) {
	key, pub := newTestKey(t)
	addr, h, ctl := sessionServer(t, map[string]string{ssh.FingerprintSHA256(pub): "alice"})

	rc, _ := connect(t, addr, key, runID)
	waitEvent(t, h.opened, "the session opened")
	if got := ctl.Sessions(); len(got) != 1 || got[0].ID != runID {
		t.Fatalf("Sessions() = %+v", got)
	}

	const why = "closed by the gateway's administrator"
	if !ctl.CloseSession(runID, why+"\nand a second line") {
		t.Fatal("CloseSession did not find the open session")
	}
	select {
	case got := <-rc.notices:
		if got != why+" and a second line" {
			t.Errorf("the client was told %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the client was never told its session was closed")
	}
	ended := make(chan struct{})
	go func() { _ = rc.cl.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("a closed session's connection stayed open")
	}
	e := waitEvent(t, h.closed, "the session was closed")
	if e.End != SessionClosedByHost || !strings.HasPrefix(e.Reason, why) {
		t.Errorf("closed %+v", e)
	}
	if got := ctl.Sessions(); len(got) != 0 {
		t.Errorf("a closed session is still listed: %+v", got)
	}

	if _, refused := connect(t, addr, key, runID); !strings.HasPrefix(refused, why) {
		t.Errorf("the closed run came back and was answered %q", refused)
	}
	if _, refused := connect(t, addr, key, "fedcba9876543210"); refused != "" {
		t.Errorf("a new run of the same key was refused: %q", refused)
	}
	if ctl.CloseSession("ffffffffffffffff", why) {
		t.Error("CloseSession reported an unknown session as closed")
	}
}

// Withdrawing a key ends everything it has open, sessions and the connections
// that present none (a shared datapath), each told why.
func TestClosingAKeyEndsAllItHasOpen(t *testing.T) {
	key, pub := newTestKey(t)
	other, otherPub := newTestKey(t)
	fp := ssh.FingerprintSHA256(pub)
	addr, h, ctl := sessionServer(t, map[string]string{fp: "alice", ssh.FingerprintSHA256(otherPub): "bob"})

	run, _ := connect(t, addr, key, runID)
	shared, _ := connect(t, addr, key, "")
	bob, _ := connect(t, addr, other, "fedcba9876543210")
	waitEvent(t, h.opened, "a session opened")
	waitEvent(t, h.opened, "a session opened")

	if n := ctl.CloseKey(fp, "your key was removed"); n != 2 {
		t.Errorf("CloseKey ended %d, want the session and the shared connection", n)
	}
	for _, rc := range []*runConn{run, shared} {
		select {
		case got := <-rc.notices:
			if got != "your key was removed" {
				t.Errorf("told %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a connection of the withdrawn key was not told")
		}
	}
	if e := waitEvent(t, h.closed, "alice's session was closed"); e.Who != "alice" || e.End != SessionClosedByHost {
		t.Errorf("closed %+v", e)
	}
	if _, _, err := bob.cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Errorf("another key's session was touched: %v", err)
	}
}

// A session id belongs to the key that opened it: another key presenting it
// neither joins it nor learns it was closed.
func TestAnotherKeyCannotJoinASession(t *testing.T) {
	key, pub := newTestKey(t)
	other, otherPub := newTestKey(t)
	addr, h, ctl := sessionServer(t, map[string]string{ssh.FingerprintSHA256(pub): "alice", ssh.FingerprintSHA256(otherPub): "mallory"})

	rc, _ := connect(t, addr, key, runID)
	waitEvent(t, h.opened, "the session opened")
	intruder, refused := connect(t, addr, other, runID)
	if refused != "" {
		t.Errorf("the intruder learnt something: %q", refused)
	}
	ctl.CloseSession(runID, "closed")
	if e := waitEvent(t, h.closed, "the session was closed"); e.Who != "alice" {
		t.Errorf("closed %+v", e)
	}
	if got := ctl.Sessions(); len(got) != 0 {
		t.Errorf("the intruder's connection kept the session open: %+v", got)
	}
	if _, refused := connect(t, addr, other, runID); refused != "" {
		t.Errorf("the closed session's reason went to another key: %q", refused)
	}
	if _, refused := connect(t, addr, key, runID); refused != "closed" {
		t.Errorf("the closed session's own key was answered %q", refused)
	}
	intruder.cl.Close()
	rc.cl.Close()
}

// A Served event names the session serving it, so a host can attach a name to
// the run on its list.
func TestServedNamesItsSession(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)
	fwd.st = &connState{session: runID}

	s.announce(fwd, "alice", "serve-name orders-svc 80:41001 takeover", "dynamic")
	if e := waitServed(t, h); e.Session != runID {
		t.Errorf("Session = %q, want %q", e.Session, runID)
	}
}

// A revoke on an agent that admits by approval ends the member's runs the same
// way, in words that say who revoked them.
func TestARevokedMemberIsToldWhoRevokedThem(t *testing.T) {
	store := &memStore{}
	addr := startServer(t, newTestInviteHost(t, store))
	fra, _ := newTestKey(t)
	admin, err := join(t, addr, fra, &admissionClient{name: "francois"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ali, aliPub := newTestKey(t)
	if cl, err := join(t, addr, ali, &admissionClient{name: "alice"}); err == nil {
		cl.Close()
	}
	if got := run(t, admin, "accept "+shortFP(ssh.FingerprintSHA256(aliPub))); !strings.HasPrefix(got, "ok accepted alice ") {
		t.Fatalf("accept answered %q", got)
	}

	alice, refused := connect(t, addr, ali, runID)
	if refused != "" {
		t.Fatalf("a member's run was refused: %s", refused)
	}
	run(t, admin, "revoke "+ssh.FingerprintSHA256(aliPub))
	select {
	case got := <-alice.notices:
		if got != "your key is no longer a member: revoked by francois" {
			t.Errorf("the revoked member was told %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the revoked member was never told")
	}
}

// A gateway with several replicas closes a session on all of them; one the run
// never reached still refuses it when the client, having missed the notice,
// reconnects there.
func TestAReplicaThatNeverSawTheSessionStillRefusesIt(t *testing.T) {
	key, pub := newTestKey(t)
	addr, h, ctl := sessionServer(t, map[string]string{ssh.FingerprintSHA256(pub): "alice"})

	if ctl.CloseSession(runID, "closed by the gateway's administrator") {
		t.Error("CloseSession reported a session this agent never had as open here")
	}
	if _, refused := connect(t, addr, key, runID); refused != "closed by the gateway's administrator" {
		t.Errorf("the closed run reached this replica and was answered %q", refused)
	}
	noEvent(t, h.opened, "a closed session opened", 100*time.Millisecond)
}
