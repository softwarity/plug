package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// slowReader yields data in small chunks with a tiny delay, to exercise the
// progress animation across several frames.
type slowReader struct {
	data []byte
	pos  int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	time.Sleep(2 * time.Millisecond)
	end := s.pos + 4096
	if end > len(s.data) {
		end = len(s.data)
	}
	n := copy(p, s.data[s.pos:end])
	s.pos += n
	return n, nil
}

func TestDownloadProgressBar(t *testing.T) {
	updateHold = 0 // don't sleep in the test

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	src := &slowReader{data: make([]byte, 300*1024)}
	for i := range src.data {
		src.data[i] = byte(i)
	}
	data, rerr := readWithProgress(src, "v9.9.9", true) // force the animated path

	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)

	if rerr != nil {
		t.Fatalf("readWithProgress: %v", rerr)
	}
	if len(data) != 300*1024 {
		t.Fatalf("read %d bytes, want %d", len(data), 300*1024)
	}
	got := string(out)
	for _, want := range []string{"updating v9.9.9", "█", "updated to v9.9.9", "KB"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output missing %q; got:\n%s", want, got)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{2048, "2 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
