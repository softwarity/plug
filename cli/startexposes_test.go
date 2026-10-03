package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// startExposes is the heart of -s: it arms the forwards, asks the agent for
// the names, offers to stop a session of ours that holds one, re-provisions
// after a reconnect and releases everything on teardown. These tests drive
// the whole of it over an in-memory transport (fakeSession) and read the
// verbs it sends, which is the contract the agent sees.

// serveOK answers every verb the way a healthy agent does.
func serveOK(cmd string) (string, error) {
	switch {
	case strings.HasPrefix(cmd, "serve-name "):
		return "dynamic", nil
	case strings.HasPrefix(cmd, "unserve-name "):
		return "ok", nil
	}
	return "error: unknown command", nil
}

func exposeSpecs(raw ...string) []tunnel.ExposeSpec {
	var out []tunnel.ExposeSpec
	for _, r := range raw {
		f := strings.Split(r, ":")
		out = append(out, tunnel.ExposeSpec{Name: f[0], ClusterPort: f[1], LocalPort: f[2]})
	}
	return out
}

// A name exposing several cluster ports is ONE signpost: the agent must get
// one serve-name per NAME carrying every port pair, in the order the -s were
// given, each pair pointing at the forward sshd allocated for it. Two verbs
// for one name would read as two sessions and bounce on the liveness check.
func TestStartExposesSendsOneServeNamePerNameWithEveryPair(t *testing.T) {
	sandboxHome(t)
	f := newFakeSession(serveOK)
	installFakeSession(t, f)
	cfg := config{host: "agent.example", port: "2222",
		exposes: exposeSpecs("web:80:3000", "api:8080:4000", "web:443:3001")}

	stop, err := startExposes(cfg)
	if err != nil {
		t.Fatalf("startExposes: %v", err)
	}
	want := []string{
		"serve-name web 80:40001,443:40003 takeover",
		"serve-name api 8080:40002 takeover",
	}
	if got := f.askedPrefixed("serve-name "); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("serve-name verbs:\n got %v\nwant %v", got, want)
	}
	// Nothing was parked, so no environment is asked for.
	if asked := f.askedPrefixed("env-of"); len(asked) != 0 {
		t.Errorf("a plain -s (nothing parked) asked for an environment: %v", asked)
	}
	// Each name is recorded as held by this process on its first forward's
	// port, which is how a LATER session gets told who holds it.
	if h := servedHolder("web"); h == nil || h.port != "40001" || h.pid != os.Getpid() {
		t.Errorf("served record for web = %+v, want this pid on port 40001", h)
	}
	if h := servedHolder("api"); h == nil || h.port != "40002" {
		t.Errorf("served record for api = %+v, want port 40002", h)
	}

	stop()
	// Released on teardown, each with the port it was held on so the agent
	// can tell our release from a successor's, then the transport goes.
	want = []string{"unserve-name web 40001", "unserve-name api 40002"}
	if got := f.askedPrefixed("unserve-name "); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unserve-name verbs:\n got %v\nwant %v", got, want)
	}
	if f.closedTimes() != 1 {
		t.Errorf("transport closed %d times, want once", f.closedTimes())
	}
	if h := servedHolder("web"); h != nil {
		t.Errorf("the served record survived teardown: %+v", h)
	}
}

// A takeover whose EndpointSlice could not be deleted is said at the moment
// it bites: half the requests still reach the deployed pod, which otherwise
// reads as a flaky application for the rest of the session.
func TestStartExposesSaysWhenATakeoverIsSplit(t *testing.T) {
	sandboxHome(t)
	f := newFakeSession(func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "serve-name ") {
			return "dynamic parked split", nil
		}
		return serveOK(cmd)
	})
	installFakeSession(t, f)
	lines := captureStderr(t)
	stop, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err != nil {
		t.Fatalf("startExposes: %v", err)
	}
	stop()
	out := lines()
	for _, want := range []string{
		"took over web",
		"HALF of its requests still reach the deployed pod",
		"endpointslices",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the session never said %q:\n%s", want, out)
		}
	}
	// Parked: the deployed workload's environment is asked for, so the
	// process that takes its place gets its variables.
	if asked := f.askedPrefixed("env-ofz web"); len(asked) == 0 {
		t.Errorf("a takeover did not ask for the parked workload's environment: %v", f.verbs())
	}
}

// withHolderSeams stands in for the terminal question and the signal, and
// returns the PIDs that were signalled.
func withHolderSeams(t *testing.T, answer bool, owned error) *[]int {
	t.Helper()
	savedAsk, savedOwned, savedSignal, savedAlive := askToStop, holderOwnedByMe, signalHolder, holderAlive
	t.Cleanup(func() {
		askToStop, holderOwnedByMe, signalHolder, holderAlive = savedAsk, savedOwned, savedSignal, savedAlive
	})
	var signalled []int
	askToStop = func(*servedRecord) bool { return answer }
	holderOwnedByMe = func(int) error { return owned }
	signalHolder = func(pid int) error { signalled = append(signalled, pid); return nil }
	holderAlive = func(int) bool { return false } // gone as soon as asked
	return &signalled
}

// refusedUnlessStopped is an agent holding web for a live session on port
// 40777 until that session is signalled, then provisioning it for us.
func refusedUnlessStopped(signalled *[]int) func(string) (string, error) {
	return func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "serve-name web ") && len(*signalled) == 0 {
			return "error: web is already exposed by another live session (agent port 40777, from 10.0.0.9)", nil
		}
		return serveOK(cmd)
	}
}

// The holder is stopped when, and only when, every check passes: the local
// record names the SAME agent port the refusal does (it is that session, not
// a leftover naming a recycled PID), the person says yes, and the kernel says
// the PID is theirs. Then the name is asked for again.
func TestStartExposesStopsAHolderThatIsOurs(t *testing.T) {
	sandboxHome(t)
	forget := markServed("web", "40777", []string{"-s", "web:80:3000", "old"})
	defer forget()
	signalled := withHolderSeams(t, true, nil)
	f := newFakeSession(refusedUnlessStopped(signalled))
	installFakeSession(t, f)

	stop, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err != nil {
		t.Fatalf("startExposes: %v", err)
	}
	defer stop()
	if len(*signalled) != 1 || (*signalled)[0] != os.Getpid() {
		t.Errorf("signalled %v, want exactly the recorded holder (%d)", *signalled, os.Getpid())
	}
	if got := f.askedPrefixed("serve-name web "); len(got) != 2 {
		t.Errorf("serve-name web asked %d time(s), want twice (refused, then after the holder stopped): %v", len(got), got)
	}
}

// A PID that belongs to another account is never signalled, whatever the
// record says and whatever the person answered: the record is stale, its PID
// reused, and on macOS the signal would go out as root.
func TestStartExposesNeverSignalsAHolderOfAnotherAccount(t *testing.T) {
	sandboxHome(t)
	forget := markServed("web", "40777", []string{"-s", "web:80:3000", "old"})
	defer forget()
	signalled := withHolderSeams(t, true, errors.New("PID 4242 belongs to uid 501, not to you"))
	f := newFakeSession(refusedUnlessStopped(signalled))
	installFakeSession(t, f)

	_, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err == nil {
		t.Fatal("the session started although the name is held and the holder could not be stopped")
	}
	if len(*signalled) != 0 {
		t.Errorf("a process of another account was signalled: %v", *signalled)
	}
	for _, want := range []string{"could not stop the session holding it", "belongs to uid 501"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q: %v", want, err)
		}
	}
	if f.closedTimes() != 1 {
		t.Errorf("the transport was not closed on failure (%d)", f.closedTimes())
	}
}

// A record on ANOTHER port is not the holder: the question is never asked,
// nothing is signalled, and the refusal names what this machine knows.
func TestStartExposesDoesNotOfferToStopARecordOnAnotherPort(t *testing.T) {
	sandboxHome(t)
	forget := markServed("web", "40001", []string{"-s", "web:80:3000", "old"})
	defer forget()
	signalled := withHolderSeams(t, true, nil)
	askToStop = func(*servedRecord) bool {
		t.Error("asked whether to stop a session the refusal does not name")
		return false
	}
	f := newFakeSession(refusedUnlessStopped(signalled))
	installFakeSession(t, f)

	_, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err == nil {
		t.Fatal("the session started on a held name")
	}
	if len(*signalled) != 0 {
		t.Errorf("signalled %v on a record that is not the holder", *signalled)
	}
	if !strings.Contains(err.Error(), "held on this machine") {
		t.Errorf("the refusal does not point at the local record: %v", err)
	}
}

// After a reconnect every forward of a name re-arms on a fresh port; the name
// is re-provisioned ONCE, with all of its ports current, and released on
// teardown under the port it is now held on.
func TestStartExposesReprovisionsAfterAReconnectWithTheReallocatedPorts(t *testing.T) {
	sandboxHome(t)
	savedSettle := rearmSettle
	rearmSettle = 20 * time.Millisecond
	t.Cleanup(func() { rearmSettle = savedSettle })
	f := newFakeSession(serveOK)
	installFakeSession(t, f)

	stop, err := startExposes(config{exposes: exposeSpecs("web:80:3000", "web:443:3001")})
	if err != nil {
		t.Fatalf("startExposes: %v", err)
	}
	// The wave: both members re-arm within a beat of each other.
	f.exposed[0].reconnect("41001")
	f.exposed[1].reconnect("41002")
	f.waitFor(t, "the re-provisioning", func(cmd string) bool {
		return cmd == "serve-name web 80:41001,443:41002 takeover"
	})
	// Let a second pass, if one was queued, go by: it must not happen.
	time.Sleep(5 * rearmSettle)
	if got := f.askedPrefixed("serve-name "); len(got) != 2 {
		t.Errorf("serve-name sent %d times, want 2 (startup, then ONE for the whole wave): %v", len(got), got)
	}
	stop()
	if got := f.askedPrefixed("unserve-name "); len(got) != 1 || got[0] != "unserve-name web 41001" {
		t.Errorf("released as %v, want under the re-allocated port", got)
	}
}

// A name the agent refuses fails the session, and what was provisioned
// before it is released: a signpost for name N must not survive a failure on
// N+1. Same for an answer that is not "dynamic".
func TestStartExposesDropsWhatItProvisionedWhenALaterNameFails(t *testing.T) {
	for _, c := range []struct{ reply, wantErr string }{
		{"error: no such service account", "api: agent: no such service account"},
		{"static", `agent answered "static"`},
	} {
		sandboxHome(t)
		f := newFakeSession(func(cmd string) (string, error) {
			if strings.HasPrefix(cmd, "serve-name api ") {
				return c.reply, nil
			}
			return serveOK(cmd)
		})
		installFakeSession(t, f)
		_, err := startExposes(config{exposes: exposeSpecs("web:80:3000", "api:8080:4000")})
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%q: err = %v, want %q", c.reply, err, c.wantErr)
		}
		if got := f.askedPrefixed("unserve-name "); len(got) != 1 || got[0] != "unserve-name web 40001" {
			t.Errorf("%q: released %v, want the name provisioned before the failure", c.reply, got)
		}
		if h := servedHolder("web"); h != nil {
			t.Errorf("%q: the served record for web outlived the failed session: %+v", c.reply, h)
		}
		if f.closedTimes() != 1 {
			t.Errorf("%q: transport closed %d times, want once", c.reply, f.closedTimes())
		}
	}
}

// A forward the agent refuses to open (a port another instance holds) fails
// the session before any name is asked for.
func TestStartExposesFailsWhenAForwardCannotBeArmed(t *testing.T) {
	sandboxHome(t)
	f := newFakeSession(serveOK)
	f.exposeErr = errors.New("agent refused to open a forward for web:80")
	installFakeSession(t, f)
	_, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err == nil || !strings.Contains(err.Error(), "refused to open a forward") {
		t.Fatalf("err = %v", err)
	}
	if got := f.askedPrefixed("serve-name "); len(got) != 0 {
		t.Errorf("a name was asked for without a forward to point it at: %v", got)
	}
	if f.closedTimes() != 1 {
		t.Errorf("transport closed %d times, want once", f.closedTimes())
	}
}

// The wait for a cluster to schedule the name is not charged to the command:
// a probe that fails up front moves to the background, the session starts,
// and the check keeps trying until the name answers.
func TestStartExposesProvesASlowPathInTheBackground(t *testing.T) {
	sandboxHome(t)
	shortVerifyBudget(t)
	savedUpfront := exposeVerifyUpfront
	exposeVerifyUpfront = time.Millisecond
	t.Cleanup(func() { exposeVerifyUpfront = savedUpfront })
	var mu sync.Mutex
	probes := 0
	f := newFakeSession(serveOK)
	f.verifyWith = func(tunnel.ExposeSpec) func(time.Duration) error {
		return func(time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			probes++
			if probes < 3 {
				return errors.New("Name does not resolve")
			}
			return nil
		}
	}
	installFakeSession(t, f)

	started := time.Now()
	stop, err := startExposes(config{exposes: exposeSpecs("web:80:3000")})
	if err != nil {
		t.Fatalf("startExposes: %v", err)
	}
	defer stop()
	if time.Since(started) > exposeVerifyBudget {
		t.Errorf("startExposes blocked for %s on a name still coming up", time.Since(started))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := probes
		mu.Unlock()
		if n >= 3 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Errorf("the path was not probed until it answered (probes = %d)", probes)
}
