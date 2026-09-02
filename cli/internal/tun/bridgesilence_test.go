package tun

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// The bridge is the datapath. When its read side ends, packets keep going in and
// nothing comes back: every name still resolves to a fake address that now
// answers nothing, so the session looks alive and reaches nothing. That is the
// hardest shape to diagnose from outside, and it used to happen without a word.
//
// The device disappearing under it is not hypothetical: a laptop waking is the
// ordinary way it happens.
func TestADeadDatapathSaysSo(t *testing.T) {
	var mu sync.Mutex
	var said []string
	log := logfn(func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		said = append(said, f)
	})

	for name, err := range map[string]error{
		"the device went away": errors.New("input/output error"),
		"a wrapped failure":    &os.PathError{Op: "read", Path: "/dev/tun0", Err: errors.New("EIO")},
	} {
		said = nil
		b := &bridge{log: log, dev: failingDevice{err: err}}
		b.toStack()
		mu.Lock()
		got := strings.Join(said, "\n")
		mu.Unlock()
		if got == "" {
			t.Errorf("%s: the datapath died and nothing was said. Names resolve, nothing connects, "+
				"and the log holds no reason", name)
			continue
		}
		if !strings.Contains(got, "restart the session") {
			t.Errorf("%s: the report does not tell the reader what to do: %s", name, got)
		}
	}
}

// A clean close is how a session ENDS. Saying it every time would put a failure
// line in the log of every successful run, which is the fastest way to teach
// people to ignore the log.
func TestAClosedDeviceIsNotAFailure(t *testing.T) {
	var said []string
	log := logfn(func(f string, a ...any) { said = append(said, f) })
	for _, err := range []error{os.ErrClosed, io.EOF} {
		said = nil
		b := &bridge{log: log, dev: failingDevice{err: err}}
		b.toStack()
		if len(said) != 0 {
			t.Errorf("a normal shutdown (%v) was reported as a failure: %v", err, said)
		}
	}
}

// failingDevice is a TUN device that only ever fails, which is the state the
// bridge has to survive and report rather than the state it is written for.
type failingDevice struct{ err error }

func (d failingDevice) File() *os.File                         { return nil }
func (d failingDevice) Read([][]byte, []int, int) (int, error) { return 0, d.err }
func (d failingDevice) Write([][]byte, int) (int, error)       { return 0, d.err }
func (d failingDevice) MTU() (int, error)                      { return 1500, nil }
func (d failingDevice) Name() (string, error)                  { return "utunTest", nil }
func (d failingDevice) Events() <-chan wgtun.Event             { return nil }
func (d failingDevice) Close() error                           { return nil }
func (d failingDevice) BatchSize() int                         { return 1 }
