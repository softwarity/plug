package tun

import (
	"sync/atomic"
	"testing"
	"time"
)

type stubResolver struct {
	found, ok bool
	delay     time.Duration
	asked     *atomic.Int32
}

func (s stubResolver) ResolveInCluster(string) (bool, bool) {
	if s.asked != nil {
		s.asked.Add(1)
	}
	time.Sleep(s.delay)
	return s.found, s.ok
}

// Each question is bounded at three seconds by the agent's own budget, and they
// used to be asked one after another: an unknown short name cost three seconds
// per sluggish cluster. This sits on the resolution path, so that time is paid by
// whatever the user just typed.
func TestASlowClusterDoesNotDelayTheOthers(t *testing.T) {
	var asked atomic.Int32
	start := time.Now()
	found, answered := askEveryCluster([]clusterNameResolver{
		stubResolver{ok: true, delay: 3 * time.Second, asked: &asked}, // the real budget
		stubResolver{ok: true, delay: 3 * time.Second, asked: &asked},
		stubResolver{found: true, ok: true, asked: &asked},
	}, "api")
	took := time.Since(start)

	if !found || !answered {
		t.Fatalf("found=%v answered=%v, want the cluster that holds the name to end it", found, answered)
	}
	if took > time.Second {
		t.Errorf("waited %v while one cluster already had the answer", took)
	}
	// Every cluster is asked, but the call returns the moment one answers, so the
	// others may not have been scheduled yet. Waiting for the count is the
	// assertion; reading it straight after the return was a race in the TEST, and
	// it failed the first time for that reason rather than for anything wrong
	// with the code.
	deadline := time.Now().Add(2 * time.Second)
	for asked.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := asked.Load(); n != 3 {
		t.Errorf("asked %d clusters, want all three: asking them in turn is the cost this removes", n)
	}
}

// "Nobody has it" and "nobody could answer" lead to different decisions upstream,
// so every reply is waited for when none of them holds the name.
func TestNobodyHasItIsNotNobodyAnswered(t *testing.T) {
	found, answered := askEveryCluster([]clusterNameResolver{
		stubResolver{ok: true}, stubResolver{ok: true},
	}, "api")
	if found {
		t.Error("a name no cluster holds was reported as found")
	}
	if !answered {
		t.Error("clusters that answered were reported as not having answered, which mints a name " +
			"the caller was told nothing about")
	}

	found, answered = askEveryCluster([]clusterNameResolver{
		stubResolver{ok: false}, stubResolver{ok: false},
	}, "api")
	if found || answered {
		t.Errorf("found=%v answered=%v, want both false when no cluster could answer at all", found, answered)
	}
}

// A mix: one cannot answer, one can and does not have it. Somebody answered.
func TestOneSilentClusterDoesNotHideTheOthersAnswer(t *testing.T) {
	if _, answered := askEveryCluster([]clusterNameResolver{
		stubResolver{ok: false}, stubResolver{ok: true},
	}, "api"); !answered {
		t.Error("one cluster unable to answer made the other's answer invisible")
	}
}

func TestNoClustersAnswersNothing(t *testing.T) {
	if found, answered := askEveryCluster(nil, "api"); found || answered {
		t.Errorf("found=%v answered=%v with no clusters attached", found, answered)
	}
}
