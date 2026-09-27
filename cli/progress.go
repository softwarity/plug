package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// progress is one line of stderr that follows a step that takes a while - a
// volume being mounted, its helper scheduled somewhere - redrawn in place
// with what is happening and how long it has been, when stderr is a
// terminal; plain lines when it is not (a CI log, a redirect), so nothing is
// lost and nothing is garbled. stderr is plug's own: the command owns stdin
// and stdout, and every [plug] line already goes there.
//
//	p := startProgress("mounting /data of geo")
//	p.step("helper starting")          // …mounting /data of geo: helper starting (3s)
//	p.done("mounted at /tmp/x (4.2s)")  // the final line, kept
type progress struct {
	mu     sync.Mutex
	label  string
	now    string // the current step
	start  time.Time
	tty    bool
	stop   chan struct{}
	closed bool
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func startProgress(label string) *progress {
	p := &progress{label: label, start: time.Now(), stop: make(chan struct{}), tty: stderrIsTerminal()}
	if p.tty {
		go p.spin()
	} else {
		info("%s…", label)
	}
	return p
}

func stderrIsTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd())) && os.Getenv("TERM") != "dumb"
}

// step names what is happening now. On a terminal it is the next redraw;
// otherwise a line, but only when it changed, so a status polled every few
// seconds does not fill a log with the same words.
func (p *progress) step(s string) {
	p.mu.Lock()
	changed := s != p.now
	p.now = s
	p.mu.Unlock()
	if !p.tty && changed && s != "" {
		info("%s: %s", p.label, s)
	}
	if p.tty {
		p.draw(spinner[int(time.Since(p.start)/(100*time.Millisecond))%len(spinner)])
	}
}

// done ends the line with its final word and the time it took.
func (p *progress) done(final string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stop)
	p.mu.Unlock()
	if p.tty {
		fmt.Fprintf(os.Stderr, "\r\033[K")
	}
	info("%s: %s (%s)", p.label, final, elapsed(p.start))
}

func (p *progress) spin() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	i := 0
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.draw(spinner[i%len(spinner)])
			i++
		}
	}
}

func (p *progress) draw(glyph string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	line := "[plug] " + glyph + " " + p.label
	if p.now != "" {
		line += ": " + p.now
	}
	line += " (" + elapsed(p.start) + ")"
	p.mu.Unlock()
	if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 8 && len([]rune(line)) > w-1 {
		r := []rune(line)
		line = string(r[:w-2]) + "…"
	}
	fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
}

func elapsed(since time.Time) string {
	d := time.Since(since)
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
