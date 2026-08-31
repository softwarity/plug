package agent

import (
	"fmt"
	"strings"
	"testing"
)

// The boot sweep is what brings back what a previous session parked: containers
// restarted, a Swarm service scaled back, a Service repointed. Absorbing its
// panic is right - an embedder must not die because an orchestrator hiccuped -
// but absorbing it in SILENCE left the agent announcing "ready" one line later
// while somebody's deployment stayed at zero replicas, with nothing anywhere
// naming the sweep that never finished.
func TestAnAbsorbedPanicIsStillReported(t *testing.T) {
	var said []string
	runQuietly("the boot sweep", func(f string, a ...any) {
		said = append(said, fmt.Sprintf(f, a...))
	}, func() { panic("the docker socket went away") })

	if len(said) == 0 {
		t.Fatal("the sweep panicked and nothing was said: the agent would announce ready over a parked workload")
	}
	for _, want := range []string{"boot sweep", "docker socket went away", "still be stopped"} {
		if !strings.Contains(said[0], want) {
			t.Errorf("the report does not name %q, so nobody can act on it: %s", want, said[0])
		}
	}
}

// And it must not propagate: not dying in the embedder's process is the whole
// reason the absorbing exists.
func TestAnAbsorbedPanicDoesNotEscape(t *testing.T) {
	runQuietly("the boot sweep", func(string, ...any) {}, func() { panic("boom") })
	// Reaching this line is the assertion.
}

// A caller with no logger must not turn a panic into a nil dereference, which
// would defeat the absorbing at the exact moment it is needed.
func TestAbsorbingWithoutALoggerIsSafe(t *testing.T) {
	runQuietly("the boot sweep", nil, func() { panic("boom") })
}
