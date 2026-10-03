package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Kubernetes backend, against the fake API server: a served name is a
// Service plus the Endpoints that make it reach this pod; a takeover repoints
// a real Service in place; the sweep lingers, reaps and restores. Each test
// names the bug it would have caught.

const (
	testNS    = "shop"
	testPodIP = "10.244.1.7"
)

// k8sRequests filters a journal to the requests on one path, query dropped.
func k8sRequests(log []string, method, path string) int {
	n := 0
	for _, l := range log {
		if m, rest, _ := strings.Cut(l, " "); m == method && strings.Split(rest, "?")[0] == path {
			n++
		}
	}
	return n
}

// A fresh name: a Service with no selector (a selector names a role, the
// session lives in one pod) and an Endpoints object naming THIS pod on the
// session's agent port. Would have caught: a Service with a selector past one
// replica (a lottery), Endpoints on the cluster port rather than the agent
// port, a port name the Endpoints did not repeat.
func TestK8sServeCreatesAServiceWithEndpointsOnThisPod(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t)

	pairs := []portPair{{cluster: "8081", agent: "41017"}, {cluster: "25", agent: "41018"}}
	if said := verbReply(t, func() { k8sServe("web", pairs) }); said != "dynamic" {
		t.Fatalf("serve answered %q, want \"dynamic\"", said)
	}
	svc, ok := fa.service("web")
	if !ok {
		t.Fatal("no Service was created")
	}
	if svc.Metadata.Labels[k8sManaged] != "plug" || svc.Metadata.Annotations[sessionOwnerLabel] != testPodIP+":41017" {
		t.Errorf("the Service must be plug's and say whose: labels %v, annotations %v", svc.Metadata.Labels, svc.Metadata.Annotations)
	}
	if len(svc.Spec.Selector) != 0 {
		t.Errorf("a plug Service carries no selector, got %v", svc.Spec.Selector)
	}
	if svc.Spec.ClusterIP == "" {
		t.Error("the fake hands out a ClusterIP at creation; the Service has none, so the create never reached it")
	}
	if len(svc.Spec.Ports) != 2 || svc.Spec.Ports[0].Name != "p8081" || svc.Spec.Ports[0].Port != 8081 || svc.Spec.Ports[1].Port != 25 {
		t.Errorf("ports = %+v, want one per exposure, named p<port>", svc.Spec.Ports)
	}
	ep, ok := fa.endpoints("web")
	if !ok || len(ep.Subsets) != 1 {
		t.Fatalf("the name reaches this pod through its Endpoints, got %+v", ep)
	}
	if len(ep.Subsets[0].Addresses) != 1 || ep.Subsets[0].Addresses[0].IP != testPodIP {
		t.Errorf("endpoints address = %+v, want this pod", ep.Subsets[0].Addresses)
	}
	if len(ep.Subsets[0].Ports) != 2 || ep.Subsets[0].Ports[0].Port != 41017 || ep.Subsets[0].Ports[0].Name != "p8081" {
		t.Errorf("endpoints ports = %+v, want the agent ports under the Service's port names", ep.Subsets[0].Ports)
	}
}

// The 2026-10-03 bug: a plug Service left lingering by an unserve, or orphaned
// by a restart, must be taken over IN PLACE. Its ClusterIP is what every
// caller cached for the DNS TTL; a delete-and-recreate hands the name a fresh
// one and every pooled connection a dead address.
func TestK8sServeReclaimsALingeringServiceWithoutChangingItsClusterIP(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t)
	fa.add("services", k8sObject("Service", "web",
		map[string]string{k8sManaged: "plug"},
		map[string]string{lingerLabel: lingerStamp()},
		map[string]any{
			"clusterIP": "10.96.0.42",
			"ports":     []map[string]any{{"name": "p80", "port": 80, "targetPort": 39000}},
		}))
	before, _ := fa.service("web")

	pairs := []portPair{{cluster: "80", agent: "41017"}}
	if said := verbReply(t, func() { k8sServe("web", pairs) }); said != "dynamic" {
		t.Fatalf("reclaim answered %q, want \"dynamic\"", said)
	}
	after, ok := fa.service("web")
	if !ok {
		t.Fatal("the Service is gone")
	}
	if after.Spec.ClusterIP != "10.96.0.42" {
		t.Errorf("ClusterIP changed from 10.96.0.42 to %q: every caller that cached it lost the name", after.Spec.ClusterIP)
	}
	if after.Metadata.UID != before.Metadata.UID {
		t.Error("the Service was recreated (new uid) instead of patched in place")
	}
	if _, lingering := after.Metadata.Annotations[lingerLabel]; lingering {
		t.Error("a reclaimed Service is served again: the linger stamp must go")
	}
	if after.Metadata.Annotations[sessionOwnerLabel] != testPodIP+":41017" {
		t.Errorf("the new session must own it, got %v", after.Metadata.Annotations)
	}
	if len(after.Spec.Ports) != 1 || after.Spec.Ports[0].Name != "p80" || after.Spec.Ports[0].TargetPort != float64(41017) {
		t.Errorf("ports must be repointed at the new session's port under the same name, got %+v", after.Spec.Ports)
	}
	if ep, ok := fa.endpoints("web"); !ok || len(ep.Subsets) != 1 || ep.Subsets[0].Addresses[0].IP != testPodIP {
		t.Errorf("the reclaimed name must reach this pod, endpoints %+v", ep)
	}
	log := fa.requests()
	if k8sRequests(log, "DELETE", "/api/v1/namespaces/"+testNS+"/services/web") != 0 {
		t.Errorf("the Service was deleted on the way:\n%s", strings.Join(log, "\n"))
	}
	if k8sRequests(log, "POST", "/api/v1/namespaces/"+testNS+"/services") != 1 {
		t.Errorf("one create (the 409 that finds the name taken), never a second:\n%s", strings.Join(log, "\n"))
	}
}

// A plug Service whose owner stops answering is set to LINGER, not deleted:
// the commonest reason a boot sweep finds a silent owner is that the agent
// itself just restarted, and the session is seconds from re-arming the name.
// Lingering keeps the ClusterIP for that re-arm; the Endpoints go so the name
// refuses rather than swallows. Past the grace the next sweep drops it. A
// Service whose owner answers is not touched at all. Would have caught: the
// sweep that deleted on silence and handed every re-arm a fresh address.
func TestK8sGCLingersASilentOwnerThenReapsPastTheGrace(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t, "10.244.2.9:41018")
	for _, s := range []struct{ name, owner string }{{"web", "10.244.1.7:41017"}, {"api", "10.244.2.9:41018"}} {
		fa.add("services", k8sObject("Service", s.name,
			map[string]string{k8sManaged: "plug"}, map[string]string{sessionOwnerLabel: s.owner},
			map[string]any{"ports": []map[string]any{{"name": "p80", "port": 80, "targetPort": 41017}}}))
		fa.add("endpoints", k8sEndpointsFor(s.name, strings.Split(s.owner, ":")[0], []portPair{{cluster: "80", agent: "41017"}}))
	}
	before, _ := fa.service("web")

	if said := stderrOf(t, k8sGC); said != "" {
		t.Errorf("an ordinary sweep stays quiet, got:\n%s", said)
	}
	web, ok := fa.service("web")
	if !ok {
		t.Fatal("the Service of a silent owner was deleted; it should linger")
	}
	if web.Spec.ClusterIP != before.Spec.ClusterIP || web.Metadata.UID != before.Metadata.UID {
		t.Error("lingering must keep the Service, and its address, as they are")
	}
	if web.Metadata.Annotations[lingerLabel] == "" {
		t.Error("a lingering Service carries the stamp its grace is counted from")
	}
	if _, owned := web.Metadata.Annotations[sessionOwnerLabel]; owned {
		t.Error("a lingering name belongs to nobody: the owner annotation must go")
	}
	if _, has := fa.endpoints("web"); has {
		t.Error("a lingering Service must refuse connections: its Endpoints must go")
	}
	api, _ := fa.service("api")
	if api.Metadata.Annotations[sessionOwnerLabel] != "10.244.2.9:41018" || api.Metadata.Annotations[lingerLabel] != "" {
		t.Errorf("a live session's Service must be left alone, got annotations %v", api.Metadata.Annotations)
	}
	if _, has := fa.endpoints("api"); !has {
		t.Error("a live session's Endpoints were cut")
	}

	// Nobody came back: move the stamp past the grace and sweep again.
	fa.edit("services", "web", func(obj map[string]any) {
		anyMap(anyMap(obj["metadata"])["annotations"])[lingerLabel] =
			strconv.FormatInt(time.Now().Add(-lingerGrace-time.Minute).Unix(), 10)
	})
	k8sGC()
	if _, still := fa.service("web"); still {
		t.Error("a linger past its grace must be reaped")
	}
	if _, still := fa.service("api"); !still {
		t.Error("the live session's Service was reaped with it")
	}
}

// A takeover of a REAL Service: repointed in place (selector dropped, ports
// retargeted under their own names, Endpoints naming this pod), its original
// spec in the receipt annotation, and the EndpointSlices Kubernetes' controller
// built for the workload removed, since kube-proxy would otherwise split the
// traffic between the agent and the pod still running. Then the restore puts
// the selector and ports back and drops the receipt. Would have caught: a
// receipt without the original ports, a renamed port that an Ingress could no
// longer find, a controller slice left beside plug's, a restore that kept the
// receipt.
func TestK8sTakeoverParksARealServiceAndRestoresIt(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t)
	fa.add("services", k8sObject("Service", "shop", nil, nil, map[string]any{
		"clusterIP": "10.96.0.7",
		"selector":  map[string]string{"app": "shop"},
		"ports":     []map[string]any{{"name": "http", "port": 80, "targetPort": 8080}},
	}))
	controllerSlice := map[string]string{
		"kubernetes.io/service-name": "shop", "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io",
	}
	fa.add("endpointslices", k8sObject("EndpointSlice", "shop-abc12", controllerSlice, nil, nil))
	fa.add("endpointslices", k8sObject("EndpointSlice", "other-def34", map[string]string{
		"kubernetes.io/service-name": "other", "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io",
	}, nil, nil))

	pairs := []portPair{{cluster: "80", agent: "41017"}}
	if said := verbReply(t, func() { k8sServe("shop", pairs) }); said != "dynamic parked" {
		t.Fatalf("takeover answered %q, want \"dynamic parked\"", said)
	}
	parked, ok := fa.service("shop")
	if !ok {
		t.Fatal("the Service is gone")
	}
	if parked.Spec.ClusterIP != "10.96.0.7" {
		t.Errorf("a takeover keeps the ClusterIP, got %q", parked.Spec.ClusterIP)
	}
	if len(parked.Spec.Selector) != 0 {
		t.Errorf("a parked Service carries no selector (the Endpoints name the pod), got %v", parked.Spec.Selector)
	}
	if len(parked.Spec.Ports) != 1 || parked.Spec.Ports[0].Name != "http" || parked.Spec.Ports[0].TargetPort != float64(41017) {
		t.Errorf("the port keeps its own name, retargeted at the session: got %+v", parked.Spec.Ports)
	}
	var receipt k8sReceipt
	if err := json.Unmarshal([]byte(parked.Metadata.Annotations[k8sParkedAnn]), &receipt); err != nil {
		t.Fatalf("no readable receipt on the parked Service: %v (annotations %v)", err, parked.Metadata.Annotations)
	}
	if receipt.Selector["app"] != "shop" || !strings.Contains(string(receipt.Ports), `"targetPort":8080`) || receipt.Owner != testPodIP+":41017" {
		t.Errorf("the receipt must hold the original selector and ports and name its owner, got %+v", receipt)
	}
	if ep, ok := fa.endpoints("shop"); !ok || ep.Subsets[0].Addresses[0].IP != testPodIP || ep.Subsets[0].Ports[0].Port != 41017 || ep.Subsets[0].Ports[0].Name != "http" {
		t.Errorf("the parked name must reach this pod under the Service's port name, endpoints %+v", ep)
	}
	if fa.get("endpointslices", "shop-abc12") != nil {
		t.Error("the controller's slice for the workload was left beside plug's: half the requests still reach the pod")
	}
	if fa.get("endpointslices", "other-def34") == nil {
		t.Error("another Service's slice was removed")
	}

	if said := verbReply(t, func() { k8sUnserve("shop") }); said != "ok" {
		t.Fatalf("unserve answered %q", said)
	}
	restored, _ := fa.service("shop")
	if restored.Spec.Selector["app"] != "shop" || len(restored.Spec.Selector) != 1 {
		t.Errorf("the restore must put the original selector back, got %v", restored.Spec.Selector)
	}
	if len(restored.Spec.Ports) != 1 || restored.Spec.Ports[0].TargetPort != float64(8080) || restored.Spec.Ports[0].Name != "http" {
		t.Errorf("the restore must put the original ports back, got %+v", restored.Spec.Ports)
	}
	for _, k := range []string{k8sParkedAnn, sessionOwnerLabel} {
		if _, still := restored.Metadata.Annotations[k]; still {
			t.Errorf("a restored Service carries no %s annotation, got %v", k, restored.Metadata.Annotations)
		}
	}
	if restored.Spec.ClusterIP != "10.96.0.7" || restored.Metadata.UID != parked.Metadata.UID {
		t.Error("park and restore both happen in place: same object, same address")
	}
}

// The Service in front of the agent itself selects the agent's pods. Repointing
// it would send the name the developer reaches the agent by into one session's
// forward and park the agent as the "workload". The k8s shape of the "takeover
// parked the agent itself" bug: refused, and nothing patched.
func TestK8sServeRefusesToParkTheAgentsOwnService(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t)
	fa.add("services", k8sObject("Service", "plug", nil, nil, map[string]any{
		"selector": map[string]string{"app": "plug"},
		"ports":    []map[string]any{{"name": "ssh", "port": 22, "targetPort": 2222}},
	}))
	before, _ := fa.service("plug")

	said := verbReply(t, func() { k8sServe("plug", []portPair{{cluster: "22", agent: "41017"}}) })
	if !strings.HasPrefix(said, "error:") || !strings.Contains(said, "selects this agent's own pods") {
		t.Fatalf("serving the agent's own name answered %q, want a refusal", said)
	}
	after, _ := fa.service("plug")
	if after.Spec.Selector["app"] != "plug" || len(after.Metadata.Annotations) != 0 || after.Metadata.UID != before.Metadata.UID {
		t.Errorf("the agent's Service was touched: %+v", after)
	}
	if k8sRequests(fa.requests(), "PATCH", "/api/v1/namespaces/"+testNS+"/services/plug") != 0 {
		t.Error("the refusal must come before any patch")
	}
}

// A parked Service whose restore the API refuses stays parked WITH its
// receipt, the failure is said once per boot rather than once a minute, and a
// later sweep that succeeds restores it and says so. Would have caught: a
// sweep that dropped the error (a Service pointing at a dead session for ever,
// nothing in the log), or one that cleared the receipt on failure.
func TestK8sGCSaysAFailedRestoreOnceAndKeepsTheReceipt(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	liveSessions(t) // the owner in the receipt does not answer
	resetGcNotes()
	receipt, err := k8sSignReceipt("", "10.244.1.7:41017", k8sReceipt{
		Selector: map[string]string{"app": "shop"},
		Ports:    json.RawMessage(`[{"name":"http","port":80,"targetPort":8080}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	fa.add("services", k8sObject("Service", "shop", nil,
		map[string]string{k8sParkedAnn: receipt, sessionOwnerLabel: "10.244.1.7:41017"},
		map[string]any{
			"clusterIP": "10.96.0.7",
			"ports":     []map[string]any{{"name": "http", "port": 80, "targetPort": 41017}},
		}))

	fa.refuseWith(func(method, path string) (int, string) {
		if method == "PATCH" && strings.HasSuffix(path, "/services/shop") {
			return 500, "etcdserver: request timed out"
		}
		return 0, ""
	})
	said := stderrOf(t, func() { k8sGC(); k8sGC(); k8sGC() })
	if n := strings.Count(said, "stays parked"); n != 1 {
		t.Errorf("three failing sweeps must say the failure once, said it %d times:\n%s", n, said)
	}
	stuck, _ := fa.service("shop")
	if stuck.Metadata.Annotations[k8sParkedAnn] != receipt {
		t.Errorf("the receipt must survive a failed restore, got %v", stuck.Metadata.Annotations)
	}

	fa.refuseWith(nil)
	said = stderrOf(t, k8sGC)
	restored, _ := fa.service("shop")
	if restored.Spec.Selector["app"] != "shop" {
		t.Errorf("the sweep did not restore the Service once the API allowed it, selector %v", restored.Spec.Selector)
	}
	if _, still := restored.Metadata.Annotations[k8sParkedAnn]; still {
		t.Error("a restored Service carries no receipt")
	}
	if !strings.Contains(said, "restored, after an earlier failure") {
		t.Errorf("the recovery closes the earlier note, got:\n%s", said)
	}
}
