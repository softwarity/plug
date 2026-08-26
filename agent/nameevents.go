package agent

// Telling the Host what is being served.
//
// It is the third thing Host asks for, and until this file nothing fired it:
// Served and Unserved were declared, implemented as no-ops standalone, and never
// called. A gateway linking the agent in would have compiled, run, and shown an
// empty state page forever.
//
// What the parent can observe: the verbs run as a SUBPROCESS
// (Config.VerbCommand), so the process holding the Host never touches the
// orchestrator itself. It knows the request (serve-name <name> <ports> takeover)
// and it reads the one line the verb answers. Between the two, every field of
// NameEvent is accounted for.
//
// Parked included, and it costs nothing: the answer already says it. A verb that
// provisioned the name answers "dynamic", and "dynamic parked" when it had to set
// a deployed workload aside to make room. The CLI has always read that second
// word to warn the developer (socks_run.go); the Host now learns it from exactly
// the same word, so a state page and a terminal cannot disagree about whether
// somebody's deployment is currently stopped.

import (
	"strings"
	"sync"
	"time"
)

// hostEventQueue is how many events may be waiting on a slow Host before the
// agent starts dropping them. Deep enough for a burst of sessions arriving
// together, shallow enough that a Host which stopped consuming is noticed.
const hostEventQueue = 64

// post hands one Host callback to the delivery goroutine.
//
// Not called inline, and not one goroutine per event either. Host.Served is
// documented as best effort and forbidden from failing a session, so a gateway
// that hangs on its own database must not hang someone's `plug -s`. One
// goroutine keeps the ORDER, which matters: Served then Unserved for the same
// name is a state page that ends empty, the reverse is one that ends wrong.
func (s *sshServer) post(f func()) {
	s.evOnce.Do(func() {
		s.ev = make(chan func(), hostEventQueue)
		go func() {
			for fn := range s.ev {
				s.deliver(fn)
			}
		}()
	})
	select {
	case s.ev <- f:
	default:
		// Dropping beats blocking: the alternative is a stalled session.
		s.note("host is not keeping up with name events; dropped one")
	}
}

// deliver runs one callback, absorbing whatever the Host does. A panic in an
// embedder's callback would otherwise take the whole agent down, which is the
// opposite of "this is information, never authorisation".
func (s *sshServer) deliver(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			s.note("host event panicked: %v", r)
		}
	}()
	fn()
}

// announce reports a finished verb to the Host. command is what the client
// asked for, reply the single line the verb answered.
//
// Only a verb that SUCCEEDED gets here, and success is not enough on its own:
// unserve-name answers "ok reassigned" when the name has already been granted to
// a newer session, and that caller must not tell the Host a name stopped being
// served when somebody else is serving it. That distinction lives in the answer,
// which is why the answer is read.
func (s *sshServer) announce(fwd *forwardSet, who, command, reply string) {
	f := strings.Fields(command)
	if len(f) < 2 {
		return
	}
	switch f[0] {
	case "serve-name":
		if len(f) < 3 {
			return
		}
		// The protocol, not a guess at it: "dynamic", optionally followed by
		// "parked". Anything else is a refusal or an agent too old for the verb,
		// and answer() exits 0 for both, so the reply is the only thing that
		// tells them apart. Matched the same way the CLI matches it.
		fields := strings.Fields(reply)
		if len(fields) == 0 || fields[0] != "dynamic" {
			return
		}
		name, ports := f[1], clusterPorts(f[2])
		fwd.hold(name, ports)
		ev := NameEvent{
			Name:   name,
			Who:    who,
			Ports:  ports,
			Parked: containsWord(fields[1:], "parked"),
			Since:  time.Now(),
		}
		s.post(func() { s.host.Served(ev) })
	case "unserve-name":
		name := f[1]
		// Success is "ok", alone or followed by a note. An error line exits 0
		// like everything else here, and must not withdraw anything: the name is
		// still served, by whoever the release failed to displace.
		if !strings.HasPrefix(reply, "ok") {
			return
		}
		// "ok reassigned" means a newer session holds it: the name is still
		// served, by someone else, and that someone's Served already went out.
		if strings.Contains(reply, "reassigned") {
			fwd.drop(name)
			return
		}
		if !fwd.drop(name) {
			// This connection never served it. Releasing a name it does not
			// hold is legitimate (a leftover from a dead session), but the Host
			// was never told it was served, so there is nothing to withdraw.
			return
		}
		s.post(func() { s.host.Unserved(name) })
	}
}

// containsWord reports whether the answer carries a bare word, so "parked" is
// read as a note on the verdict and never as part of a longer token.
func containsWord(fields []string, word string) bool {
	for _, f := range fields {
		if f == word {
			return true
		}
	}
	return false
}

// clusterPorts pulls the CLUSTER side out of the <cluster>:<agent>[,…] argument.
// The agent side is an internal allocation nobody outside this process can use;
// the cluster side is the port a colleague would actually connect to, which is
// what a state page must show.
func clusterPorts(arg string) []string {
	var out []string
	for _, pair := range strings.Split(arg, ",") {
		if c, _, ok := strings.Cut(pair, ":"); ok && c != "" {
			out = append(out, c)
		}
	}
	return out
}

// heldNames is what one connection currently serves, so its teardown can
// withdraw exactly those names and no others.
type heldNames struct {
	mu sync.Mutex
	m  map[string][]string
}

func (h *heldNames) hold(name string, ports []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string][]string{}
	}
	h.m[name] = ports
}

// drop forgets a name and reports whether this connection held it.
func (h *heldNames) drop(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.m[name]
	delete(h.m, name)
	return ok
}

// takeAll empties the set and returns what was in it, so a teardown can only
// ever withdraw each name once however many ways the connection ends.
func (h *heldNames) takeAll() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.m))
	for n := range h.m {
		names = append(names, n)
	}
	h.m = nil
	return names
}

// withdrawAll tells the Host that everything this connection served has stopped.
//
// This is the case that matters most and the one an explicit unserve-name never
// covers: a laptop that sleeps, a process killed with SIGKILL, a cable pulled.
// The forwards are released the same way and for the same reason (closeAll), so
// the two ride together.
func (s *sshServer) withdrawAll(fwd *forwardSet) {
	for _, name := range fwd.takeAll() {
		n := name
		s.post(func() { s.host.Unserved(n) })
	}
}

// tailWriter keeps the last bytes written through it, and passes everything on
// untouched. The verb's stdout belongs to the client; this only listens.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// lastLine is the verb's answer: verbs print exactly one line, but a backend
// warning ahead of it must not be mistaken for the verdict.
func (t *tailWriter) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimRight(string(t.buf), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
