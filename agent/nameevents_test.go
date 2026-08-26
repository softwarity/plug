package agent

// What the Host is told, and when. These events are the only thing a gateway
// linking the agent in can build a state page from, and every case below is one
// where the obvious implementation says something false.

import (
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// recordingHost captures events instead of acting on them.
type recordingHost struct {
	served   chan NameEvent
	unserved chan string
	panicOn  string // the one name whose delivery blows up
}

func newRecordingHost() *recordingHost {
	return &recordingHost{served: make(chan NameEvent, 8), unserved: make(chan string, 8)}
}

func (h *recordingHost) HostKey() (ssh.Signer, error)        { return nil, nil }
func (h *recordingHost) Verify(ssh.PublicKey) (string, bool) { return "", true }
func (h *recordingHost) Served(e NameEvent) {
	if e.Name == h.panicOn {
		panic("the host blew up")
	}
	h.served <- e
}
func (h *recordingHost) Unserved(name string) { h.unserved <- name }

func testServer(h Host) (*sshServer, *forwardSet) {
	s := &sshServer{host: h, logf: func(string, ...any) {}}
	return s, &forwardSet{srv: s}
}

func waitServed(t *testing.T, h *recordingHost) NameEvent {
	t.Helper()
	select {
	case e := <-h.served:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("the host was never told the name is served")
		return NameEvent{}
	}
}

func expectNothing(t *testing.T, h *recordingHost) {
	t.Helper()
	select {
	case e := <-h.served:
		t.Fatalf("the host was told %q is served, and it is not", e.Name)
	case n := <-h.unserved:
		t.Fatalf("the host was told %q stopped being served, and nothing said so", n)
	case <-time.After(150 * time.Millisecond):
	}
}

// The event names the person the Host recognised and the CLUSTER ports, which
// are the two things a state page shows. The agent-side port is an internal
// allocation nobody outside this process could connect to.
func TestServedNamesThePersonAndTheClusterPorts(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001,25:41002 takeover", "dynamic")

	e := waitServed(t, h)
	if e.Name != "fpl-svc" {
		t.Errorf("Name = %q, want fpl-svc", e.Name)
	}
	if e.Who != "alice" {
		t.Errorf("Who = %q, want alice", e.Who)
	}
	if len(e.Ports) != 2 || e.Ports[0] != "80" || e.Ports[1] != "25" {
		t.Errorf("Ports = %v, want the cluster side [80 25]", e.Ports)
	}
	if e.Since.IsZero() {
		t.Error("Since is zero, so a state page cannot say how long it has been served")
	}
}

// answer() exits 0 whatever it prints, error lines included. Reading only the
// exit status would announce every refused serve as a success.
func TestARefusedServeAnnouncesNothing(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover",
		`error: "fpl-svc" is already exposed by another live session`)

	expectNothing(t, h)
}

// The case the exit status cannot see: unserve-name answers "ok reassigned" when
// a NEWER session already holds the name. Telling the Host the name stopped
// being served would blank a page while somebody is still serving it.
func TestAReassignedNameIsNotWithdrawn(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic")
	waitServed(t, h)
	s.announce(fwd, "alice", "unserve-name fpl-svc 41001", "ok reassigned")

	expectNothing(t, h)
}

func TestUnserveWithdrawsTheNameOnce(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic")
	waitServed(t, h)
	s.announce(fwd, "alice", "unserve-name fpl-svc 41001", "ok")

	select {
	case n := <-h.unserved:
		if n != "fpl-svc" {
			t.Fatalf("Unserved(%q), want fpl-svc", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host was never told the name was released")
	}
	// A second release is a leftover from a dead session, not a second event.
	s.announce(fwd, "alice", "unserve-name fpl-svc 41001", "ok")
	expectNothing(t, h)
}

// The failure mode an explicit unserve never covers, and the reason the set is
// tracked per connection at all: the laptop sleeps, the cable goes, the process
// is killed. The forwards are released the same way and so is the name.
func TestADeadConnectionWithdrawsWhatItServed(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic")
	s.announce(fwd, "alice", "serve-name audgw 25:41002 takeover", "dynamic")
	waitServed(t, h)
	waitServed(t, h)

	s.withdrawAll(fwd)

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case n := <-h.unserved:
			got[n] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d names withdrawn, want 2", len(got))
		}
	}
	if !got["fpl-svc"] || !got["audgw"] {
		t.Errorf("withdrew %v, want both names", got)
	}
	// Whatever else happens to the connection, each name goes out once.
	s.withdrawAll(fwd)
	expectNothing(t, h)
}

// Host callbacks are the embedder's code. "Information, never authorisation"
// means a broken one costs its own event and nothing else.
func TestAPanickingHostDoesNotTakeTheAgentDown(t *testing.T) {
	h := newRecordingHost()
	h.panicOn = "fpl-svc"
	s, fwd := testServer(h)

	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic")
	// Queued behind it, so it can only be delivered after the panic happened.
	s.announce(fwd, "alice", "serve-name audgw 25:41002 takeover", "dynamic")
	if e := waitServed(t, h); e.Name != "audgw" {
		t.Fatalf("after a panicking host, got %q", e.Name)
	}
}

func TestClusterPortsTakesTheClusterSide(t *testing.T) {
	for _, c := range []struct {
		arg  string
		want []string
	}{
		{"80:41001", []string{"80"}},
		{"80:41001,25:41002,110:41003", []string{"80", "25", "110"}},
		{"", nil},
		{"garbage", nil},
	} {
		got := clusterPorts(c.arg)
		if len(got) != len(c.want) {
			t.Errorf("clusterPorts(%q) = %v, want %v", c.arg, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("clusterPorts(%q) = %v, want %v", c.arg, got, c.want)
				break
			}
		}
	}
}

// A backend warning printed ahead of the verdict must not be read as the
// verdict: that would turn "ok reassigned" into an unnoticed withdrawal.
func TestTailReadsTheLastLineNotTheFirst(t *testing.T) {
	tw := &tailWriter{max: 4096}
	tw.Write([]byte("warning: could not reach the registry\n"))
	tw.Write([]byte("ok reassigned\n"))
	if got := tw.lastLine(); got != "ok reassigned" {
		t.Fatalf("lastLine = %q, want the verdict", got)
	}
}

func TestTailKeepsOnlyTheEndOfAChattyVerb(t *testing.T) {
	tw := &tailWriter{max: 32}
	for i := 0; i < 100; i++ {
		tw.Write([]byte("noise noise noise\n"))
	}
	tw.Write([]byte("ok\n"))
	if got := tw.lastLine(); got != "ok" {
		t.Fatalf("lastLine = %q, want ok", got)
	}
	if len(tw.buf) > 32 {
		t.Fatalf("buffer grew to %d bytes, the cap is 32", len(tw.buf))
	}
}

// Parked is the one field the parent could have guessed at and must not: it says
// a colleague's deployment is stopped right now. The verb already reports it,
// and the word is the same one the CLI reads to warn the developer, so a state
// page and a terminal cannot end up disagreeing.
func TestParkedIsCarriedFromTheVerbsAnswer(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  bool
	}{
		{"dynamic parked", true},
		{"dynamic", false},
		{"dynamic  parked", true}, // whatever spacing the answer arrives with
	} {
		h := newRecordingHost()
		s, fwd := testServer(h)
		s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", c.reply)
		if got := waitServed(t, h).Parked; got != c.want {
			t.Errorf("answer %q gave Parked = %v, want %v", c.reply, got, c.want)
		}
	}
}

// "parked" has to be a word of its own. A future note that merely contains those
// letters must not report somebody's deployment as stopped.
func TestOnlyTheBareWordParkedCounts(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)
	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic unparked")
	if e := waitServed(t, h); e.Parked {
		t.Error("a note that merely contains \"parked\" was read as a parked workload")
	}
}

// An agent too old for the verb answers through /bin/sh, and a refusal exits 0
// like everything else. Neither is a name being served.
func TestOnlyTheProtocolAnswerCountsAsServed(t *testing.T) {
	for _, reply := range []string{
		"sh: serve-name: not found",
		`error: "fpl-svc" is already exposed by another live session`,
		"",
		"ok", // the unserve verdict, never the serve one
	} {
		h := newRecordingHost()
		s, fwd := testServer(h)
		s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", reply)
		expectNothing(t, h)
	}
}

// The mirror image on release: a failure exits 0 and must withdraw nothing, or
// a name that is still served disappears from the page.
func TestAFailedReleaseWithdrawsNothing(t *testing.T) {
	h := newRecordingHost()
	s, fwd := testServer(h)
	s.announce(fwd, "alice", "serve-name fpl-svc 80:41001 takeover", "dynamic")
	waitServed(t, h)
	s.announce(fwd, "alice", "unserve-name fpl-svc 41001",
		`error: reading the Service "fpl-svc" to release it: timeout`)
	expectNothing(t, h)
}
