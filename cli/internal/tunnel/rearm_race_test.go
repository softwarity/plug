package tunnel

import (
	"sync"
	"testing"
)

// The state a name's mappings share across a re-arm wave: every member writes
// its freshly allocated port while the re-provisioner reads all of them to
// rebuild the signpost, and OnRearm can land mid-reconnect. Under -race this
// fails outright without the mutex.
//
// Every goroutine is bounded by a fixed count — no stop channel, no spin: a
// test must not be able to outlive its assertion.
func TestExposedSharedStateIsRaceFree(t *testing.T) {
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
