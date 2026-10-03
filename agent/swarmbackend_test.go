package agent

import (
	"strings"
	"testing"
)

// The Swarm backend, against the fake Engine as a manager: a takeover scales
// the real service to zero behind a signpost SERVICE, the boot sweep scales it
// back, and the agent's own service is never parked. Each test names the bug
// it would have caught.

// swarmAgent is a task of the stack's plug service on the stack's overlay: the
// shape dockerSelf reports on Swarm, handed in directly. The stash directory
// and the secret-reading patience are pointed away from their defaults: a
// takeover reads the parked service's secrets first, and with no task to read
// from it would otherwise wait fifteen seconds for one.
func swarmAgent(t *testing.T, fe *fakeEngine) selfInfo {
	t.Helper()
	fe.setSwarm(true)
	fe.addNetwork("shop_net", "overlay", false)
	plug := fe.addService("shop_plug", nil, 1, map[string][]string{"shop_net": {"plug"}})
	oldDir, oldPatience := swarmSecretStashDir, swarmSecretsPatience
	swarmSecretStashDir, swarmSecretsPatience = t.TempDir(), 0
	t.Cleanup(func() { swarmSecretStashDir, swarmSecretsPatience = oldDir, oldPatience })
	return selfInfo{
		id: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", name: "shop_plug.1.task1",
		service: "shop_plug", serviceID: plug.ID, image: "example/plug:test",
		nets: []netRef{{name: "shop_net", overlay: true, addrs: []string{"10.0.1.5"}}},
	}
}

// A takeover of a stack service: the signpost service is created on the
// overlay with the alias, carrying the receipt (which service, how many
// replicas), BEFORE the real service is scaled to zero. Would have caught: a
// receipt recording the wrong replica count, a relay to the service VIP rather
// than the task, a scale-down with no signpost yet in DNS.
func TestSwarmServeTakeoverScalesTheServiceToZeroAndCreatesTheSignpost(t *testing.T) {
	fe := newFakeEngine(t)
	self := swarmAgent(t, fe)
	web := fe.addService("shop_web", nil, 2, map[string][]string{"shop_net": {"web"}})
	liveSessions(t)

	pairs := []portPair{{cluster: "80", agent: "41017"}}
	if said := verbReply(t, func() { swarmServe("web", pairs, self) }); said != "dynamic parked" {
		t.Fatalf("takeover answered %q, want \"dynamic parked\"", said)
	}

	sp, ok := fe.spec(signpostName("web"))
	if !ok {
		t.Fatal("no signpost service was created")
	}
	want := map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: "shop_plug",
		sessionOwnerLabel: "shop_plug.1.task1:41017", parkedServiceLabel: "shop_web", parkedReplicasLabel: "2",
	}
	for k, v := range want {
		if sp.Labels[k] != v {
			t.Errorf("signpost label %s = %q, want %q", k, sp.Labels[k], v)
		}
	}
	if got := strings.Join(sp.TaskTemplate.ContainerSpec.Command, " "); got != "/usr/local/bin/plug-agent signpost 80 shop_plug.1.task1:41017" {
		t.Errorf("signpost command = %q: it must relay to the TASK holding the session, never the service VIP", got)
	}
	if len(sp.TaskTemplate.Networks) != 1 || sp.TaskTemplate.Networks[0].Target != "shop_net" ||
		strings.Join(sp.TaskTemplate.Networks[0].Aliases, ",") != "web" {
		t.Errorf("the signpost must join the stack overlay with the name as alias, got %+v", sp.TaskTemplate.Networks)
	}
	if sp.Mode.Replicated == nil || sp.Mode.Replicated.Replicas != 1 {
		t.Errorf("one signpost task, got %+v", sp.Mode)
	}
	if ws, _ := fe.spec(web.ID); ws.Mode.Replicated == nil || ws.Mode.Replicated.Replicas != 0 {
		t.Errorf("the real service was not parked (scaled to 0), got %+v", ws.Mode)
	}
	if ps, _ := fe.spec(self.serviceID); ps.Mode.Replicated == nil || ps.Mode.Replicated.Replicas != 1 {
		t.Error("the agent's own service was touched by a takeover of another name")
	}
	log := fe.requests()
	create, scale := index(log, "POST", "/services/create"), index(log, "POST", "/services/"+web.ID+"/update")
	if create < 0 || scale < 0 || create > scale {
		t.Errorf("the signpost must exist before the workload is scaled down (create at %d, scale at %d):\n%s", create, scale, strings.Join(log, "\n"))
	}
}

// The Swarm shape of the 2026-10-02 bug: the receipt is in the signpost
// service's labels, so a sweep whose scale-back the manager refuses must keep
// the signpost, say so once, and succeed on a later pass; deleting it would
// leave a deployed service at zero replicas with nothing recording why.
func TestDockerGCKeepsTheSwarmSignpostWhenTheScaleBackFails(t *testing.T) {
	fe := newFakeEngine(t)
	self := swarmAgent(t, fe)
	agentIs(t, self)
	liveSessions(t)
	resetGcNotes()
	web := fe.addService("shop_web", nil, 0, map[string][]string{"shop_net": {"web"}})
	sp := fe.addService(signpostName("web"), map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: "shop_plug",
		sessionOwnerLabel: "shop_plug.1.task0:41017", parkedServiceLabel: "shop_web", parkedReplicasLabel: "2",
	}, 1, map[string][]string{"shop_net": {"web"}})

	fe.refuseWith(func(method, path string) (int, string) {
		if method == "POST" && path == "/services/shop_web/update" {
			return 500, "rpc error: code = Unavailable desc = raft: no leader"
		}
		return 0, ""
	})
	said := stderrOf(t, func() { dockerGC(); dockerGC() })
	if fe.service(sp.ID) == nil {
		t.Fatal("the sweep deleted the signpost service although the scale-back failed: the receipt is gone with it")
	}
	if ws, _ := fe.spec(web.ID); ws.Mode.Replicated == nil || ws.Mode.Replicated.Replicas != 0 {
		t.Errorf("the service is reported scaled although the manager refused, got %+v", ws.Mode)
	}
	if n := strings.Count(said, "could not scale"); n != 1 {
		t.Errorf("the failure must be said once per boot, not %d times:\n%s", n, said)
	}

	fe.refuseWith(nil)
	said = stderrOf(t, dockerGC)
	if ws, _ := fe.spec(web.ID); ws.Mode.Replicated == nil || ws.Mode.Replicated.Replicas != 2 {
		t.Errorf("the sweep did not scale the parked service back to its receipt's count, got %+v", ws.Mode)
	}
	if fe.service(sp.ID) != nil {
		t.Error("the signpost service outlived a successful scale-back")
	}
	if !strings.Contains(said, "after an earlier failure") {
		t.Errorf("the recovery closes the earlier note, got:\n%s", said)
	}
}

// A lingering signpost (a clean unserve, holding the VIP warm) is not an
// orphan: within its grace the sweep leaves it, whoever stamped it, and past
// the grace it goes. Would have caught: a boot sweep that read every linger as
// a leftover and killed the address the linger exists to keep.
func TestDockerGCHonoursTheSwarmLingerGrace(t *testing.T) {
	fe := newFakeEngine(t)
	self := swarmAgent(t, fe)
	agentIs(t, self)
	liveSessions(t)
	fresh := fe.addService(signpostName("web"), map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: "some-earlier-task", lingerLabel: lingerStamp(),
	}, 1, map[string][]string{"shop_net": {"web"}})
	stale := fe.addService(signpostName("api"), map[string]string{
		signpostLabel: "1", signpostNameLabel: "api", signpostOwnerLabel: "shop_plug", lingerLabel: "1",
	}, 1, map[string][]string{"shop_net": {"api"}})

	dockerGC()
	if fe.service(fresh.ID) == nil {
		t.Error("a linger within its grace was swept, and its VIP with it")
	}
	if fe.service(stale.ID) != nil {
		t.Error("a linger past its grace must be reaped")
	}
}

// The agent's own service answers to `plug`, the alias its stack gives it.
// Scaling it to zero would stop the task holding the session AND every task
// that could ever restore it. The "takeover parked the agent itself" bug, on
// Swarm: refused before any service is created or scaled.
func TestSwarmServeRefusesToParkTheAgentsOwnService(t *testing.T) {
	fe := newFakeEngine(t)
	self := swarmAgent(t, fe)
	liveSessions(t)

	said := verbReply(t, func() { swarmServe("plug", []portPair{{cluster: "22", agent: "41017"}}, self) })
	if !strings.HasPrefix(said, "error:") || !strings.Contains(said, "this agent's own service") {
		t.Errorf("serving the agent's alias answered %q, want a refusal naming the agent's own service", said)
	}
	if ps, _ := fe.spec(self.serviceID); ps.Mode.Replicated == nil || ps.Mode.Replicated.Replicas != 1 {
		t.Fatalf("the takeover scaled the agent's service: %+v", ps.Mode)
	}
	if fe.service(signpostName("plug")) != nil {
		t.Error("a signpost was created before the refusal")
	}
	for _, l := range fe.requests() {
		if strings.HasPrefix(l, "POST /services/create") || strings.Contains(l, "/update") {
			t.Errorf("the refusal must come before anything is created or scaled, saw %q", l)
		}
	}
}
