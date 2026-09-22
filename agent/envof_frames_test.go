package agent

import (
	"bufio"
	"bytes"
	"testing"
)

// The exec stream as the API server sends it: one WebSocket frame per chunk,
// unmasked (servers never mask), the first payload byte naming the channel.
// stdout is channel 1; stderr is 2 and must not leak into the environment.
// A 126-length header and a close frame are the two shapes a small answer
// meets in practice.
func TestChannelFramesKeepStdoutOnly(t *testing.T) {
	frame := func(op byte, payload []byte) []byte {
		b := []byte{0x80 | op}
		if len(payload) < 126 {
			b = append(b, byte(len(payload)))
		} else {
			b = append(b, 126, byte(len(payload)>>8), byte(len(payload)))
		}
		return append(b, payload...)
	}
	long := bytes.Repeat([]byte("X"), 200)
	stream := bytes.Join([][]byte{
		frame(0x2, append([]byte{1}, []byte("A=1\x00")...)),
		frame(0x2, append([]byte{2}, []byte("noise on stderr")...)),
		frame(0x2, append([]byte{1}, append([]byte("B="), long...)...)),
		frame(0x2, append([]byte{1}, []byte("\x00")...)),
		frame(0x8, nil),
		frame(0x2, append([]byte{1}, []byte("after close, never read")...)),
	}, nil)
	got, err := readChannelFrames(bufio.NewReader(bytes.NewReader(stream)), 1)
	if err != nil {
		t.Fatal(err)
	}
	env := procEnviron(got)
	if len(env) != 2 || env[0] != "A=1" || env[1] != "B="+string(long) {
		t.Fatalf("got %d entries: %q", len(env), truncate(env))
	}
}

func truncate(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		if len(s) > 20 {
			s = s[:20] + "..."
		}
		out[i] = s
	}
	return out
}

// Channel 3 is the API server's verdict. A Failure there is the error the
// caller gets - "executable file not found" when the image has no cat, which
// is what a distroless target says - and a Success, or silence, is none.
func TestChannelThreeFailureBecomesTheError(t *testing.T) {
	frame := func(op byte, payload []byte) []byte { return append([]byte{0x80 | op, byte(len(payload))}, payload...) }
	failing := bytes.Join([][]byte{
		frame(0x2, append([]byte{3}, []byte(`{"status":"Failure","message":"error executing command in container: executable file not found","reason":"InternalError"}`)...)),
		frame(0x8, nil),
	}, nil)
	out, err := readChannelFrames(bufio.NewReader(bytes.NewReader(failing)), 1)
	if len(out) != 0 || err == nil || !bytes.Contains([]byte(err.Error()), []byte("executable file not found")) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	succeeding := bytes.Join([][]byte{
		frame(0x2, append([]byte{1}, []byte("A=1\x00")...)),
		frame(0x2, append([]byte{3}, []byte(`{"status":"Success"}`)...)),
		frame(0x8, nil),
	}, nil)
	out, err = readChannelFrames(bufio.NewReader(bytes.NewReader(succeeding)), 1)
	if err != nil || string(out) != "A=1\x00" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
