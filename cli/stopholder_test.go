package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stopHolder is the one place plug signals a process it did not start, and on
// macOS it does so as root. What these protect is its order: the kernel is
// asked who owns the PID BEFORE anything is sent, and the wait only begins
// once the signal is out.

// seamStopHolder replaces the account check, the signal and the liveness
// probe for the length of the test, and returns what was signalled.
func seamStopHolder(t *testing.T, owned error, alive func(int) bool) *[]int {
	t.Helper()
	savedOwned, savedSignal, savedAlive, savedTick := holderOwnedByMe, signalHolder, holderAlive, stopHolderTick
	t.Cleanup(func() {
		holderOwnedByMe, signalHolder, holderAlive, stopHolderTick = savedOwned, savedSignal, savedAlive, savedTick
	})
	var signalled []int
	holderOwnedByMe = func(int) error { return owned }
	signalHolder = func(pid int) error { signalled = append(signalled, pid); return nil }
	holderAlive = alive
	stopHolderTick = time.Millisecond
	return &signalled
}

// A PID of another account is refused before any signal goes out, with the
// account check's own words: the record is stale and says so.
func TestStopHolderNeverSignalsAnotherAccountsProcess(t *testing.T) {
	signalled := seamStopHolder(t, errors.New("PID 4242 belongs to uid 501, not to you"), func(int) bool { return true })
	err := stopHolder(&servedRecord{pid: 4242, port: "40777"})
	if err == nil || !strings.Contains(err.Error(), "belongs to uid 501") {
		t.Fatalf("err = %v, want the account check's refusal", err)
	}
	if len(*signalled) != 0 {
		t.Errorf("signalled %v although the PID is not this account's", *signalled)
	}
}

// The owner's PID is signalled once, then waited for until it is gone.
func TestStopHolderSignalsTheOwnersProcessThenWaitsForIt(t *testing.T) {
	polls := 0
	signalled := seamStopHolder(t, nil, func(int) bool {
		polls++
		return polls < 3 // gone on the third look
	})
	if err := stopHolder(&servedRecord{pid: 4242, port: "40777"}); err != nil {
		t.Fatalf("stopHolder: %v", err)
	}
	if len(*signalled) != 1 || (*signalled)[0] != 4242 {
		t.Errorf("signalled %v, want 4242 once", *signalled)
	}
	if polls != 3 {
		t.Errorf("polled %d time(s), want until the process was gone (3)", polls)
	}
}

// A holder that ignores the request is reported, not killed: SIGKILL would
// skip the teardown that restores what the session parked.
func TestStopHolderGivesUpOnAProcessThatStays(t *testing.T) {
	signalled := seamStopHolder(t, nil, func(int) bool { return true })
	err := stopHolder(&servedRecord{pid: 4242, port: "40777"})
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v, want 'still running'", err)
	}
	if len(*signalled) != 1 {
		t.Errorf("signalled %d time(s), want exactly one SIGTERM and no escalation", len(*signalled))
	}
}

// The real thing, on unix: a child of this process is this account's, so the
// kernel check passes, SIGTERM reaches it and the wait sees it go.
func TestStopHolderStopsAProcessOfThisAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stopHolder's account check and signal are unix")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on PATH")
	}
	cmd := exec.Command(sleep, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it as it goes: a zombie still answers signal 0, and the wait
	// would then outlast the test.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if err := stopHolder(&servedRecord{pid: cmd.Process.Pid, port: "40777"}); err != nil {
		t.Fatalf("stopHolder on a child of this process: %v", err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the child is still running after stopHolder returned")
	}
	if processAlive(cmd.Process.Pid) {
		t.Error("processAlive still says yes for a process that was reaped")
	}
}

// A record that is a symlink is not read: what a record leads to is a signal
// sent with the launcher's privilege, so a link somebody else planted under
// ~/.plug/served must not get to name the PID.
func TestServedHolderIgnoresASymlinkedRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the owned read refuses symlinks on unix only")
	}
	sandboxHome(t)
	if err := os.MkdirAll(servedDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "planted")
	body := "pid = " + strconv.Itoa(os.Getpid()) + "\nport = 40777\n"
	if err := os.WriteFile(elsewhere, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(servedDir(), "web")); err != nil {
		t.Skipf("cannot create a symlink here (%v)", err)
	}
	if h := servedHolder("web"); h != nil {
		t.Fatalf("a symlinked record named a holder: %+v", h)
	}
}
