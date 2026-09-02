package tunnel

import (
	"sync"
	"testing"
)

// This one guards the READER, AgentPort, and only it.
//
// Its comment used to describe the shared state as a whole, which it does not
// touch: it takes and releases the lock in the TEST rather than calling rearm or
// serve, so removing the mutex from all three writers left it green. Removing it
// from AgentPort alone does fail it, which is what it actually protects and what
// this comment now says.
//
// The writers are covered by TestRearmWaveTouchesSharedStateUnderTheLock, which
// drives the wave through rearm and serve themselves. Both are kept: they fail on
// different mutations, and a test that names its subject correctly is worth more
// than one fewer test.
func TestAgentPortIsReadUnderTheLock(t *testing.T) {
	const rounds = 2000
	members := make([]*Exposed, 4)
	for i := range members {
		members[i] = &Exposed{spec: ExposeSpec{Name: "mail", ClusterPort: "80"}, agentPort: "41000"}
	}
	var wg sync.WaitGroup
	for i, m := range members { // each member re-arming on a new port, as rearm() does
		wg.Add(1)
		go func(i int, m *Exposed) {
			defer wg.Done()
			for n := 0; n < rounds; n++ {
				m.mu.Lock()
				m.agentPort = string(rune('0'+i)) + string(rune('0'+n%10))
				m.mu.Unlock()
			}
		}(i, m)
	}
	wg.Add(1)
	go func() { // the re-provisioner reading every member's current port
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			for _, m := range members {
				_ = m.AgentPort()
			}
		}
	}()
	wg.Add(1)
	go func() { // OnRearm racing the reconnect that reads the hook
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			members[0].OnRearm(func() {})
			members[0].mu.Lock()
			h := members[0].rearmHook
			members[0].mu.Unlock()
			if h != nil {
				h()
			}
		}
	}()
	wg.Wait()
}
