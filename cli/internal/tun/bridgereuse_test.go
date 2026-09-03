package tun

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	wgtun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// recordingDevice keeps a COPY of every packet written to it. The copy is the
// whole point: the bridge hands the same backing array over and over now, so a
// device that kept the slice would be reading whatever the next packet wrote.
type recordingDevice struct {
	mu   sync.Mutex
	sent [][]byte
}

func (d *recordingDevice) Write(bufs [][]byte, offset int) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range bufs {
		d.sent = append(d.sent, append([]byte(nil), b[offset:]...))
	}
	return len(bufs), nil
}

func (d *recordingDevice) packets() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.sent...)
}

func (d *recordingDevice) File() *os.File                         { return nil }
func (d *recordingDevice) Read([][]byte, []int, int) (int, error) { return 0, os.ErrClosed }
func (d *recordingDevice) MTU() (int, error)                      { return mtu, nil }
func (d *recordingDevice) Name() (string, error)                  { return "utunTest", nil }
func (d *recordingDevice) Events() <-chan wgtun.Event             { return nil }
func (d *recordingDevice) Close() error                           { return nil }
func (d *recordingDevice) BatchSize() int                         { return 1 }

// fromStack reuses one buffer for every packet it carries, which is worth two
// allocations per packet and is safe because wireguard-go's Write consumes what
// it is given before returning. The risk that buys is a new one: with a fresh
// buffer per packet nothing could leak between them, and now everything can.
//
// A long packet followed by a short one is the shape that catches it. Reuse the
// array but forget to cut it back to the new length and the short packet arrives
// with the tail of the long one still attached, which no length check downstream
// would question because the bytes are a plausible packet. Sizes here are chosen
// so the second is shorter than the first, then the third longer than both.
func TestFromStackDoesNotLeakOnePacketIntoTheNext(t *testing.T) {
	dev := &recordingDevice{}
	ep := channel.New(8, mtu, "")
	defer ep.Close()
	b := &bridge{dev: dev, ep: ep}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.fromStack(ctx); close(done) }()

	want := [][]byte{
		bytes.Repeat([]byte{0xA1}, 1200),
		bytes.Repeat([]byte{0xB2}, 40), // shorter: the one that exposes a stale tail
		bytes.Repeat([]byte{0xC3}, 900),
	}
	// WritePackets is the direction the bridge reads: what the stack is sending
	// OUT, towards the device. InjectInbound would push them the other way.
	for _, p := range want {
		var pkts stack.PacketBufferList
		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
		pkts.PushBack(pkb)
		if _, err := ep.WritePackets(pkts); err != nil {
			t.Fatalf("queueing a packet for the bridge: %v", err)
		}
		pkts.DecRef()
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(dev.packets()) < len(want) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	got := dev.packets()
	if len(got) != len(want) {
		t.Fatalf("the bridge carried %d packets, want %d", len(got), len(want))
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Errorf("packet %d arrived %d bytes long, want %d: the reused buffer was not cut back "+
				"to this packet's length, so the previous one's tail rode along", i, len(got[i]), len(want[i]))
			continue
		}
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("packet %d arrived with different bytes than it was given", i)
		}
	}
}
