package agent

import (
	"strings"
	"testing"
)

// The Compose backend, against the fake Engine: a takeover, the boot sweep
// that undoes one, and the one takeover that must be refused. Each test names
// the bug it would have caught; until now every one of these paths was
// exercised by the e2e suite only.

// composeAgent is a Compose-deployed agent on the stack's network: the shape
// dockerSelf would report, handed in directly.
func composeAgent(fe *fakeEngine) selfInfo {
	fe.addNetwork("shop_default", "bridge", true)
	agent := fe.addContainer("shop-plug-1", true,
		map[string]string{"com.docker.compose.service": "plug", "com.docker.compose.project": "shop"},
		map[string][]string{"shop_default": {"plug"}})
	return selfInfo{
		id: agent.ID, name: "shop-plug-1", image: "example/plug:test",
		compose: "plug", project: "shop",
		nets: []netRef{{name: "shop_default", attachable: true, addrs: []string{"172.18.0.5"}}},
	}
}

// index is where a request first appears in a journal, -1 when it never does.
func index(log []string, method, pathPrefix string) int {
	for i, l := range log {
		if strings.HasPrefix(l, method+" "+pathPrefix) {
			return i
		}
	}
	return -1
}

// A takeover must park the REAL container (and only it), write the ids it
// parked into the signpost's labels, and start the signpost BEFORE stopping
// the workload so the name never has a moment with no record in DNS. Would
// have caught: a signpost created without its receipt, or a park that ran
// before the alias existed.
func TestContainerServeTakeoverParksTheWorkloadAndWritesTheReceipt(t *testing.T) {
	fe := newFakeEngine(t)
	self := composeAgent(fe)
	web := fe.addContainer("shop-web-1", true,
		map[string]string{"com.docker.compose.service": "web", "com.docker.compose.project": "shop"},
		map[string][]string{"shop_default": {"web"}})
	liveSessions(t)

	pairs := []portPair{{cluster: "80", agent: "41017"}}
	if said := verbReply(t, func() { containerServe("web", pairs, self) }); said != "dynamic parked" {
		t.Fatalf("takeover answered %q, want \"dynamic parked\"", said)
	}

	sp := fe.container(signpostName("web"))
	if sp == nil {
		t.Fatal("no signpost container was created")
	}
	if !sp.Running {
		t.Error("the signpost was created but never started")
	}
	want := map[string]string{
		signpostLabel:         "1",
		signpostNameLabel:     "web",
		signpostOwnerLabel:    "shop-plug-1",
		sessionOwnerLabel:     "shop-plug-1:41017",
		parkedContainersLabel: web.ID,
	}
	for k, v := range want {
		if sp.Labels[k] != v {
			t.Errorf("signpost label %s = %q, want %q", k, sp.Labels[k], v)
		}
	}
	if got := strings.Join(sp.Entrypoint, " "); got != "/usr/local/bin/plug-agent signpost 80 shop-plug-1:41017" {
		t.Errorf("signpost entrypoint = %q: it must relay the cluster port to THIS agent's session port", got)
	}
	if ep := sp.Networks["shop_default"]; ep == nil || len(ep.Aliases) != 1 || ep.Aliases[0] != "web" {
		t.Errorf("the signpost must carry the name as an alias on the stack network, got %+v", sp.Networks)
	}
	if fe.container(web.ID).Running {
		t.Error("the workload owning the name was not parked")
	}
	if !fe.container(self.id).Running {
		t.Error("the agent itself was stopped by its own takeover")
	}
	log := fe.requests()
	start, stop := index(log, "POST", "/containers/"+sp.ID+"/start"), index(log, "POST", "/containers/"+web.ID+"/stop")
	if start < 0 || stop < 0 || start > stop {
		t.Errorf("the signpost must be live before the workload is stopped (start at %d, stop at %d):\n%s", start, stop, strings.Join(log, "\n"))
	}
}

// The receipt lives in the signpost's labels and nowhere else. A sweep that
// cannot restart what was parked must therefore keep the signpost, say so once,
// and retry next pass; deleting it leaves a workload stopped with nothing left
// anywhere saying a session stopped it. This is the bug of 2026-10-02.
func TestDockerGCKeepsTheSignpostWhenTheRestoreFails(t *testing.T) {
	fe := newFakeEngine(t)
	self := composeAgent(fe)
	agentIs(t, self)
	liveSessions(t) // the session that parked it is gone
	resetGcNotes()
	web := fe.addContainer("shop-web-1", false, nil, map[string][]string{"shop_default": {"web"}})
	sp := fe.addContainer(signpostName("web"), true, map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: self.owner(),
		sessionOwnerLabel: "shop-plug-1:41017", parkedContainersLabel: web.ID,
	}, map[string][]string{"shop_default": {"web"}})

	fe.refuseWith(func(method, path string) (int, string) {
		if method == "POST" && path == "/containers/"+web.ID+"/start" {
			return 500, "driver failed programming external connectivity: port is already allocated"
		}
		return 0, ""
	})
	said := stderrOf(t, func() { dockerGC(); dockerGC() })

	if fe.container(sp.ID) == nil {
		t.Fatal("the sweep deleted the signpost although the restart failed: the receipt is gone with it")
	}
	if fe.container(web.ID).Running {
		t.Error("the workload is reported running although the daemon refused the start")
	}
	if n := strings.Count(said, "could not restart"); n != 1 {
		t.Errorf("the failure must be said once per boot, not %d times:\n%s", n, said)
	}

	// The daemon recovers: the next sweep restores the workload, and only
	// then removes the signpost.
	fe.refuseWith(nil)
	said = stderrOf(t, dockerGC)
	if !fe.container(web.ID).Running {
		t.Error("the sweep did not restart the parked workload once the daemon allowed it")
	}
	if fe.container(sp.ID) != nil {
		t.Error("the signpost outlived a successful restore")
	}
	if !strings.Contains(said, "after an earlier failure") {
		t.Errorf("the recovery closes the earlier note, got:\n%s", said)
	}
	log := fe.requests()
	if start, del := index(log, "POST", "/containers/"+web.ID+"/start"), index(log, "DELETE", "/containers/"+sp.ID); del >= 0 && del < start {
		t.Errorf("restore first, signpost second: the receipt must outlive the restart\n%s", strings.Join(log, "\n"))
	}
}

// The ordinary case of the same sweep: a dead session's signpost is restored
// and removed in one pass, and a LIVE session's signpost (its owner address
// answers) is left alone, workload parked as its session wants it. Would have
// caught: a sweep that restores under a session still serving the name.
func TestDockerGCRestoresADeadSessionAndKeepsALiveOne(t *testing.T) {
	fe := newFakeEngine(t)
	self := composeAgent(fe)
	agentIs(t, self)
	resetGcNotes()
	live := liveSessions(t, "shop-plug-1:41018")

	web := fe.addContainer("shop-web-1", false, nil, map[string][]string{"shop_default": {"web"}})
	api := fe.addContainer("shop-api-1", false, nil, map[string][]string{"shop_default": {"api"}})
	deadSP := fe.addContainer(signpostName("web"), true, map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: self.owner(),
		sessionOwnerLabel: "shop-plug-1:41017", parkedContainersLabel: web.ID,
	}, map[string][]string{"shop_default": {"web"}})
	liveSP := fe.addContainer(signpostName("api"), true, map[string]string{
		signpostLabel: "1", signpostNameLabel: "api", signpostOwnerLabel: self.owner(),
		sessionOwnerLabel: "shop-plug-1:41018", parkedContainersLabel: api.ID,
	}, map[string][]string{"shop_default": {"api"}})

	if said := stderrOf(t, dockerGC); said != "" {
		t.Errorf("an ordinary restore stays quiet, got:\n%s", said)
	}
	if !fe.container(web.ID).Running || fe.container(deadSP.ID) != nil {
		t.Error("the dead session's workload must be running again and its signpost gone")
	}
	if fe.container(api.ID).Running || fe.container(liveSP.ID) == nil {
		t.Error("the live session's workload must stay parked and its signpost in place")
	}

	// Its session ends without saying goodbye: the next sweep takes it.
	delete(live, "shop-plug-1:41018")
	dockerGC()
	if !fe.container(api.ID).Running || fe.container(liveSP.ID) != nil {
		t.Error("once its session stops answering, a signpost is swept and its workload restored")
	}
}

// The agent answers to names of its own (its container name, its Compose
// service alias). A takeover of one of them would stop the container the
// session's forward lives in, and the receipt would go down with the only
// process that could read it. This is the "takeover parked the agent itself"
// bug: the refusal must come before anything is created or stopped.
func TestContainerServeRefusesToParkTheAgentItself(t *testing.T) {
	fe := newFakeEngine(t)
	self := composeAgent(fe) // on the network as "plug", its Compose alias
	liveSessions(t)

	for _, name := range []string{"plug", "shop-plug-1"} {
		said := verbReply(t, func() { containerServe(name, []portPair{{cluster: "22", agent: "41017"}}, self) })
		if !strings.HasPrefix(said, "error:") || !strings.Contains(said, "this agent's own container") {
			t.Errorf("serving %q answered %q, want a refusal naming the agent's own container", name, said)
		}
		if !fe.container(self.id).Running {
			t.Fatalf("serving %q stopped the agent", name)
		}
		if fe.container(signpostName(name)) != nil {
			t.Errorf("serving %q created a signpost before refusing", name)
		}
	}
	for _, l := range fe.requests() {
		if strings.HasPrefix(l, "POST /containers/create") || strings.Contains(l, "/stop") {
			t.Errorf("the refusal must come before anything is created or stopped, saw %q", l)
		}
	}
}

// Releasing a name restores what it parked and then removes the signpost, in
// that order. Would have caught: an unserve that deleted the signpost on a
// restart it could not make.
func TestDockerUnserveRestoresThenRemoves(t *testing.T) {
	fe := newFakeEngine(t)
	self := composeAgent(fe)
	liveSessions(t)
	web := fe.addContainer("shop-web-1", false, nil, map[string][]string{"shop_default": {"web"}})
	sp := fe.addContainer(signpostName("web"), true, map[string]string{
		signpostLabel: "1", signpostNameLabel: "web", signpostOwnerLabel: self.owner(),
		sessionOwnerLabel: "shop-plug-1:41017", parkedContainersLabel: web.ID,
	}, map[string][]string{"shop_default": {"web"}})

	fe.refuseWith(func(method, path string) (int, string) {
		if method == "POST" && path == "/containers/"+web.ID+"/start" {
			return 500, "oci runtime error"
		}
		return 0, ""
	})
	if said := verbReply(t, func() { dockerUnserve("web") }); !strings.HasPrefix(said, "error:") {
		t.Fatalf("an unserve whose restore failed answered %q: the caller would believe the name released", said)
	}
	if fe.container(sp.ID) == nil {
		t.Fatal("the signpost, and the receipt in it, went although the workload could not be restarted")
	}

	fe.refuseWith(nil)
	if said := verbReply(t, func() { dockerUnserve("web") }); said != "ok" {
		t.Fatalf("unserve answered %q", said)
	}
	if !fe.container(web.ID).Running || fe.container(sp.ID) != nil {
		t.Error("the workload must be back and the signpost gone")
	}
}
