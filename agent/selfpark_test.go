package agent

import "testing"

// A takeover parks whatever owns the name, and the one owner it must never
// park is the agent itself: stopping its container, scaling its service to
// zero or repointing the Service in front of it takes the session's forward
// down with the only thing that could restore it. `plug` is a natural name to
// ask for, and the agent answers to it on every backend (its Compose service
// alias, its Swarm service, its Service), so the rule is tested on each.

func TestOwnerIsAgentOnDocker(t *testing.T) {
	self := selfInfo{
		id: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", name: "shop-plug-1",
		compose: "plug", project: "shop",
	}
	for _, c := range []struct {
		name string
		o    owner
		want bool
	}{
		{"its own container, by id", owner{id: self.id, name: "whatever"}, true},
		{"its own container, by name", owner{id: "ffff", name: "shop-plug-1"}, true},
		{"a replica of its Compose service", owner{id: "ffff", name: "shop-plug-2", compose: "plug", project: "shop"}, true},
		{"the same service name in another project", owner{id: "ffff", name: "other-plug-1", compose: "plug", project: "other"}, false},
		{"a real workload", owner{id: "ffff", name: "shop-web-1", compose: "web", project: "shop"}, false},
		{"a plain container", owner{id: "ffff", name: "web"}, false},
		{"empty ids never match", owner{}, false},
	} {
		if got := ownerIsAgent(c.o, self); got != c.want {
			t.Errorf("%s: ownerIsAgent = %v, want %v", c.name, got, c.want)
		}
	}
	// Off Swarm, a task label on the owner means nothing.
	if ownerIsAgent(owner{id: "ffff", name: "x", service: "shop_plug"}, self) {
		t.Error("a Swarm service label must not match an agent that runs no service")
	}
	// On Swarm, a sibling task of the agent's service is the agent.
	swarm := selfInfo{id: "aaaa", name: "shop_plug.1.l5vhiqbv4nqh", service: "shop_plug"}
	if !ownerIsAgent(owner{id: "bbbb", name: "shop_plug.2.x9", service: "shop_plug"}, swarm) {
		t.Error("a task of the agent's own service is the agent")
	}
	if ownerIsAgent(owner{id: "bbbb", name: "shop_web.1.x9", service: "shop_web"}, swarm) {
		t.Error("another service's task is a workload")
	}
}

func TestAgentAmongOwners(t *testing.T) {
	self := selfInfo{id: "aaaa", name: "shop-plug-1"}
	owners := []owner{{id: "bbbb", name: "shop-web-1"}, {id: "aaaa", name: "shop-plug-1"}}
	if o := agentAmongOwners(owners, self); o == nil || o.id != "aaaa" {
		t.Fatalf("the agent among the owners must be found, got %v", o)
	}
	if o := agentAmongOwners(owners[:1], self); o != nil {
		t.Fatalf("no agent among the owners, got %v", o)
	}
	if o := agentAmongOwners(nil, self); o != nil {
		t.Fatalf("no owners, got %v", o)
	}
}

func TestSwarmOwnerIsAgent(t *testing.T) {
	self := selfInfo{name: "shop_plug.1.l5vhiqbv4nqh", service: "shop_plug", serviceID: "svc-plug-id"}
	if !swarmOwnerIsAgent("svc-plug-id", "shop_plug", self) {
		t.Error("the agent's own service, by name and id")
	}
	if !swarmOwnerIsAgent("other-id", "shop_plug", self) {
		t.Error("the agent's own service, by name")
	}
	if !swarmOwnerIsAgent("svc-plug-id", "renamed", self) {
		t.Error("the agent's own service, by id")
	}
	if swarmOwnerIsAgent("svc-web-id", "shop_web", self) {
		t.Error("another service is a workload")
	}
	// Off Swarm there is no service to be.
	if swarmOwnerIsAgent("svc-plug-id", "shop_plug", selfInfo{name: "shop-plug-1"}) {
		t.Error("an agent that runs no service owns no service")
	}
}

func TestK8sSelectsAgent(t *testing.T) {
	agent := map[string]string{"app": "plug", "pod-template-hash": "abc"}
	for _, c := range []struct {
		name string
		sel  map[string]string
		want bool
	}{
		{"the manifest's own Service", map[string]string{"app": "plug"}, true},
		{"a Service selecting on every agent label", map[string]string{"app": "plug", "pod-template-hash": "abc"}, true},
		{"a real workload", map[string]string{"app": "web"}, false},
		// The fallback shape adds `app: plug` to a workload's selector and
		// leaves its other keys: those keys are what make it the workload's.
		{"a workload repointed by the selector fallback", map[string]string{"app": "plug", "app.kubernetes.io/name": "web"}, false},
		{"no selector (a plug-created name) is not the agent's", nil, false},
		{"empty selector", map[string]string{}, false},
	} {
		if got := k8sSelectsAgent(c.sel, agent); got != c.want {
			t.Errorf("%s: k8sSelectsAgent = %v, want %v", c.name, got, c.want)
		}
	}
}

// The sweep says a failed restore once per receipt and per boot, and says the
// recovery only when there was a failure to close: a minute-by-minute sweep
// must not fill the log with the same line.
func TestGcNoteOnceAndRecovered(t *testing.T) {
	gcNotedMu.Lock()
	gcNoted = map[string]bool{}
	gcNotedMu.Unlock()
	gcNoteOnce("t:a", "first")
	gcNoteOnce("t:a", "again")
	gcNotedMu.Lock()
	n := len(gcNoted)
	gcNotedMu.Unlock()
	if n != 1 {
		t.Fatalf("one key noted, got %d", n)
	}
	gcNoteRecovered("t:a", "recovered")
	gcNoteRecovered("t:a", "recovered twice")
	gcNotedMu.Lock()
	n = len(gcNoted)
	gcNotedMu.Unlock()
	if n != 0 {
		t.Fatalf("a recovery clears the key, got %d", n)
	}
}

// A Service's old target port can be the re-arming caller's own new one after
// an agent restart; the port probe must not read the caller as a rival.
func TestOwnsAgentPort(t *testing.T) {
	pairs := []portPair{{cluster: "80", agent: "41000"}, {cluster: "443", agent: "41001"}}
	if !ownsAgentPort(pairs, "41001") {
		t.Fatal("the caller's own port must be recognised")
	}
	if ownsAgentPort(pairs, "41002") || ownsAgentPort(nil, "41000") {
		t.Fatal("another port is somebody else's")
	}
}
