package main

import (
	"strings"
	"testing"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// A session the cluster ended releases nothing from here: the agent restores
// what it parked, and the teardown says so instead of warning that a workload
// is still parked.
func TestATeardownAfterTheClusterClosedTheSessionDoesNotCryWolf(t *testing.T) {
	sandboxHome(t)
	closed := false
	f := newFakeSession(func(cmd string) (string, error) {
		if closed && strings.HasPrefix(cmd, "unserve-name ") {
			return "", &tunnel.ClosedError{Addr: "agent:2222", Reason: "closed by the gateway's administrator"}
		}
		return serveOK(cmd)
	})
	installFakeSession(t, f)
	stop, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err != nil {
		t.Fatal(err)
	}
	closed = true
	out := captureStderr(t)
	stop()
	got := out()
	if strings.Contains(got, "WARNING") || !strings.Contains(got, "web: released by the cluster") {
		t.Errorf("the teardown said:\n%s", got)
	}
}

// The run's exit status is the command's, unless the cluster ended the
// session: then it is exitSessionClosed, and the last line says who and why.
func TestARunTheClusterEndedExitsWithItsOwnStatus(t *testing.T) {
	cfg := config{profile: "demo"}
	if got := finishRun(cfg, 3); got != 3 {
		t.Fatalf("an ordinary run exited %d, want the command's 3", got)
	}
	sessionEndMu.Lock()
	sessionEndErr = &tunnel.ClosedError{Addr: "agent:2222", Reason: "your key was removed"}
	sessionEndMu.Unlock()
	t.Cleanup(func() {
		sessionEndMu.Lock()
		sessionEndErr = nil
		sessionEndMu.Unlock()
	})
	out := captureStderr(t)
	code := finishRun(cfg, 0)
	if got := out(); !strings.Contains(got, "demo closed this session: your key was removed") {
		t.Errorf("the last line was:\n%s", got)
	}
	if code != exitSessionClosed {
		t.Errorf("exit %d, want %d", code, exitSessionClosed)
	}
}

// Every connection of a run presents the same id; a run never borrows another's.
func TestEachRunHasItsOwnSession(t *testing.T) {
	a, b := newRunSession(config{}), newRunSession(config{})
	if len(a.ID) != 16 || a.ID == b.ID || a.Version != version {
		t.Errorf("sessions %+v and %+v", a, b)
	}
}

// The session says what the run does: a -c reaches the cluster, a -s serves
// names too, and the core in a --dockerrun sidecar holds a container.
func TestARunSaysWhatItDoes(t *testing.T) {
	if s := newRunSession(config{}); s.Serves || s.Docker {
		t.Errorf("a -c on this machine said %+v", s)
	}
	if s := newRunSession(config{exposes: []tunnel.ExposeSpec{{}}}); !s.Serves || s.Docker {
		t.Errorf("a -s on this machine said %+v", s)
	}
	t.Setenv("PLUG_CORE_SIDECAR", "1")
	if s := newRunSession(coreConfigFromEnv()); s.Serves || !s.Docker {
		t.Errorf("a -c in a sidecar said %+v", s)
	}
	args := strings.Join(sidecarArgs(config{host: "h", port: "2222"}, "", []string{"-c"}, "amd64", "n", "img", testOwner), " ")
	if !strings.Contains(args, "-e PLUG_CORE_SIDECAR=1") {
		t.Errorf("the sidecar's core is not told it holds a container: %s", args)
	}
}

// Outside a run (the shared datapath daemon, a one-shot command) nothing
// presents a session, so nothing is opened to hold one either.
func TestOnlyARunHoldsASession(t *testing.T) {
	stop, err := holdSession(config{host: "unreachable.invalid", port: "1"})
	if err != nil {
		t.Fatalf("a config with no session dialled: %v", err)
	}
	stop()
	if got := coreConfigFromEnv().session; got.ID != "" {
		t.Errorf("a core config carries a session before runCore gives it one: %+v", got)
	}
}
