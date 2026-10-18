package tun

import (
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

// The cluster ending the session stops the command the way a SIGTERM aimed at
// plug does, and the session's teardown follows as it does then.
func TestARequestedStopEndsTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sleep(1) to run; Terminate is Kill there")
	}
	t.Cleanup(func() {
		stopCh = make(chan struct{})
		stopOnce = sync.Once{}
	})
	done := make(chan int, 1)
	go func() {
		code, _ := waitChild(exec.Command("sleep", "30"))
		done <- code
	}()
	time.Sleep(100 * time.Millisecond)
	RequestStop()
	RequestStop() // asking twice is nothing
	select {
	case code := <-done:
		if code == 0 {
			t.Errorf("a stopped command exited 0")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command kept running after the stop")
	}
}
