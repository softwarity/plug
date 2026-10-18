package agent

// Hosted and standalone, through Start as an embedder and plug-agent call it.
//
// A gateway (hosted) passes a Control and a Host that follows sessions: it
// hears every run, attaches each name to its run, and ends one with words the
// developer reads. A standalone agent passes neither: runs present their
// session and nothing hears it, and nothing on the port can end someone
// else's. The two are proven side by side, so a change that leaks one into the
// other fails here.

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// orderedHost records every event on one channel, in the order the agent
// delivered them.
type orderedHost struct {
	signer  ssh.Signer
	allowed map[string]string
	events  chan string
}

func (h *orderedHost) HostKey() (ssh.Signer, error) { return h.signer, nil }
func (h *orderedHost) Verify(key ssh.PublicKey) (string, bool) {
	who, ok := h.allowed[ssh.FingerprintSHA256(key)]
	return who, ok
}
func (h *orderedHost) Served(e NameEvent) { h.events <- "served " + e.Name + " " + e.Session }
func (h *orderedHost) Unserved(n string)  { h.events <- "unserved " + n }

// hostedHost is orderedHost following sessions too, as a gateway does.
type hostedHost struct{ orderedHost }

func (h *hostedHost) SessionOpened(e SessionEvent) { h.events <- "opened " + e.ID + " " + e.Who }
func (h *hostedHost) SessionClosed(e SessionEvent) {
	h.events <- fmt.Sprintf("closed %s %s %s", e.ID, e.End, e.Reason)
}

// startEmbedded runs Start as an embedder would, and returns once it accepts.
func startEmbedded(t *testing.T, host Host, ctl *Control, key ssh.Signer) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the agent only ever runs in a Linux container")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Start(ctx, Config{
			Host:        host,
			Addr:        addr,
			VerbCommand: []string{"/bin/sh", "-c", `printf 'dynamic parked'`},
			Logf:        func(string, ...any) {},
			Control:     ctl,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Start did not return")
		}
	})
	for i := 0; i < 100; i++ {
		if cl, err := dial(t, addr, tunnelUser, ssh.PublicKeys(key)); err == nil {
			cl.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the agent never accepted a connection")
	return ""
}

func nextEvent(t *testing.T, events chan string) string {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("the host was told nothing more")
		return ""
	}
}

func serveOver(t *testing.T, rc *runConn, name string) {
	t.Helper()
	s, err := rc.cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out, _ := s.Output("serve-name " + name + " 80:40001 takeover"); !strings.HasPrefix(string(out), "dynamic") {
		t.Fatalf("serve-name answered %q", out)
	}
}

// Hosted: the gateway hears the run open, the name it serves under that run,
// and, when it closes the run, the name withdrawn before the run's end, with
// the reason the developer read.
func TestAHostedAgentReportsAndEndsRuns(t *testing.T) {
	key, pub := newTestKey(t)
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	host := &hostedHost{orderedHost{signer: hk, allowed: map[string]string{ssh.FingerprintSHA256(pub): "alice"}, events: make(chan string, 16)}}
	ctl := NewControl()
	addr := startEmbedded(t, host, ctl, key)

	rc, refused := connect(t, addr, key, runID)
	if refused != "" {
		t.Fatalf("refused: %s", refused)
	}
	serveOver(t, rc, "checkout")
	if got := nextEvent(t, host.events); got != "opened "+runID+" alice" {
		t.Fatalf("first event %q", got)
	}
	if got := nextEvent(t, host.events); got != "served checkout "+runID {
		t.Fatalf("second event %q", got)
	}
	if !ctl.CloseSession(runID, "closed by the gateway's administrator") {
		t.Fatal("the open run was not found")
	}
	select {
	case got := <-rc.notices:
		if got != "closed by the gateway's administrator" {
			t.Errorf("the developer read %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the developer was never told")
	}
	if got := nextEvent(t, host.events); got != "unserved checkout" {
		t.Errorf("third event %q, want the name withdrawn before the run ends", got)
	}
	if got := nextEvent(t, host.events); got != "closed "+runID+" closed closed by the gateway's administrator" {
		t.Errorf("last event %q", got)
	}
}

// Standalone: the same run presents its session and works as before; the Host
// hears names and nothing about sessions; and no Control exists for anything on
// the port to end someone else's run.
func TestAStandaloneAgentNeitherReportsNorEndsRuns(t *testing.T) {
	key, pub := newTestKey(t)
	hk, err := hostKeySigner(filepath.Join(t.TempDir(), "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	host := &orderedHost{signer: hk, allowed: map[string]string{ssh.FingerprintSHA256(pub): ""}, events: make(chan string, 16)}
	addr := startEmbedded(t, host, nil, key)

	rc, refused := connect(t, addr, key, runID)
	if refused != "" {
		t.Fatalf("a standalone agent refused a run: %s", refused)
	}
	serveOver(t, rc, "checkout")
	if got := nextEvent(t, host.events); got != "served checkout "+runID {
		t.Fatalf("the host heard %q", got)
	}
	rc.bye()
	if got := nextEvent(t, host.events); got != "unserved checkout" {
		t.Fatalf("the host heard %q", got)
	}
	select {
	case got := <-host.events:
		t.Errorf("a standalone host heard %q", got)
	case <-time.After(200 * time.Millisecond):
	}
	if _, refused := connect(t, addr, key, runID); refused != "" {
		t.Errorf("a run that ended was refused on a standalone agent: %q", refused)
	}
}

// The hosts plug ships follow no session: the events are a gateway's.
func TestThePlugHostsFollowNoSession(t *testing.T) {
	for _, h := range []Host{&standaloneHost{}, &inviteHost{}} {
		if _, ok := h.(SessionHost); ok {
			t.Errorf("%T reports sessions", h)
		}
	}
}

// A Control not given to Start, or after Start returned, finds nothing and
// ends nothing.
func TestAControlWithoutAnAgentFindsNothing(t *testing.T) {
	var none *Control
	none.attach(nil)
	for _, c := range []*Control{NewControl(), none} {
		if c.CloseSession(runID, "x") || c.CloseKey("SHA256:x", "x") != 0 || c.Sessions() != nil {
			t.Errorf("%v acted with no agent", c)
		}
	}
}

// The download account is anonymous: it has no session to present.
func TestTheDownloadAccountPresentsNoSession(t *testing.T) {
	addr := startServer(t, &fakeHost{allowed: map[string]string{}})
	cl, err := dial(t, addr, downloadUser)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ok, _, err := cl.SendRequest(sessionHelloRequest, true, ssh.Marshal(helloPayload{ID: runID}))
	if err != nil || ok {
		t.Errorf("the download account presented a session: ok=%v err=%v", ok, err)
	}
}
