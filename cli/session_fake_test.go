package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// fakeSession is an in-memory sessionTransport: every verb the session sends
// is recorded and answered by a script, every -s mapping gets an agent port
// allocated in order, and a reconnect can be played on any mapping. It is
// what lets startExposes and startMounts run end to end without an sshd.
type fakeSession struct {
	mu sync.Mutex
	// reply answers one verb; nil answers "error: unknown command".
	reply func(cmd string) (string, error)
	asked []string
	// exposeErr fails the next Expose, the way a taken port would.
	exposeErr error
	exposed   []*fakeExposed
	nextPort  int
	closed    int
	localAddr string
	// dial is what DialCluster and DialClusterTimeout answer; nil refuses.
	dial func(addr string) (net.Conn, error)
	// verifyWith gives each new mapping its path check; nil passes at once.
	verifyWith func(tunnel.ExposeSpec) func(time.Duration) error
}

func newFakeSession(reply func(cmd string) (string, error)) *fakeSession {
	return &fakeSession{reply: reply, nextPort: 40001, localAddr: "10.0.0.9"}
}

// installFakeSession hands f to every dialSession for the length of the test.
func installFakeSession(t *testing.T, f *fakeSession) {
	t.Helper()
	saved := dialSession
	t.Cleanup(func() { dialSession = saved })
	dialSession = func(config) (sessionTransport, error) { return f, nil }
}

func (f *fakeSession) Exec(cmd string) (string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, cmd)
	reply := f.reply
	f.mu.Unlock()
	if reply == nil {
		return "error: unknown command", nil
	}
	return reply(cmd)
}

func (f *fakeSession) ExecAll(cmd string) (string, error) { return f.Exec(cmd) }

func (f *fakeSession) Expose(spec tunnel.ExposeSpec) (exposedMapping, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exposeErr != nil {
		return nil, f.exposeErr
	}
	e := &fakeExposed{spec: spec, port: strconv.Itoa(f.nextPort)}
	if f.verifyWith != nil {
		e.verify = f.verifyWith(spec)
	}
	f.nextPort++
	f.exposed = append(f.exposed, e)
	return e, nil
}

func (f *fakeSession) LocalAddr() string { return f.localAddr }

func (f *fakeSession) DialCluster(addr string) (net.Conn, error) {
	if f.dial == nil {
		return nil, errors.New("fake session: nothing to dial")
	}
	return f.dial(addr)
}

func (f *fakeSession) DialClusterTimeout(addr string, _ time.Duration) (net.Conn, error) {
	return f.DialCluster(addr)
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

// verbs is what was asked so far, a copy.
func (f *fakeSession) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeSession) closedTimes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// askedPrefixed is the verbs starting with prefix, in order.
func (f *fakeSession) askedPrefixed(prefix string) []string {
	var out []string
	for _, v := range f.verbs() {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}

// waitFor polls until some verb satisfies ok, or the budget runs out: the
// re-provisioning after a reconnect runs on its own goroutine.
func (f *fakeSession) waitFor(t *testing.T, what string, ok func(cmd string) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range f.verbs() {
			if ok(v) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never asked; verbs so far: %v", what, f.verbs())
}

// fakeExposed is one armed mapping: its port is what the session reads when
// building serve-name, and reconnect re-allocates it the way a transport
// death does, then fires the re-arm hook like the accept loop would.
type fakeExposed struct {
	mu     sync.Mutex
	spec   tunnel.ExposeSpec
	port   string
	hook   func()
	verify func(time.Duration) error
}

func (e *fakeExposed) Spec() tunnel.ExposeSpec { return e.spec }

func (e *fakeExposed) AgentPort() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.port
}

func (e *fakeExposed) OnRearm(hook func()) {
	e.mu.Lock()
	e.hook = hook
	e.mu.Unlock()
}

func (e *fakeExposed) Verify(d time.Duration) error {
	e.mu.Lock()
	v := e.verify
	e.mu.Unlock()
	if v == nil {
		return nil
	}
	return v(d)
}

func (e *fakeExposed) reconnect(newPort string) {
	e.mu.Lock()
	e.port = newPort
	hook := e.hook
	e.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// captureStderr routes the [plug] lines into a buffer until the returned
// function is called, which restores stderr and hands the lines back. Only
// for code paths that write from the test's own goroutine: a background
// writer would race the restore.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	var once sync.Once
	var out string
	return func() string {
		once.Do(func() {
			os.Stderr = saved
			w.Close()
			var buf bytes.Buffer
			io.Copy(&buf, r)
			r.Close()
			out = buf.String()
		})
		return out
	}
}
