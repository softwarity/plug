//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A record whose process is gone is a leftover — a session killed with -9 never
// ran its teardown. Pointing at it would be worse than saying nothing, since
// PIDs get reused and the advice is "kill this".
//
// Unix-only: the liveness probe is signal 0. Windows has no equivalent and
// os.FindProcess can still open a terminated process, so the record there is
// explicitly a hint the command line confirms — there is no behaviour to assert.
func TestServedHolderIgnoresARecordWhoseProcessIsGone(t *testing.T) {
	sandboxHome(t)

	// A pid that certainly existed and certainly does not any more: run the test
	// binary with a filter that matches nothing, and wait for it.
	done := exec.Command(os.Args[0], "-test.run=TestThisNameMatchesNoTest")
	if err := done.Run(); err != nil {
		t.Fatalf("could not spawn a throwaway process: %v", err)
	}
	dead := done.Process.Pid

	if err := os.MkdirAll(servedDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := "pid = " + itoa(dead) + "\ndir = /tmp\ncmd = nest start --watch\n"
	if err := os.WriteFile(filepath.Join(servedDir(), "fpl-svc"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := servedHolder("fpl-svc"); h != "" {
		t.Errorf("a dead holder (pid %d) was reported as live:\n%s", dead, h)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
