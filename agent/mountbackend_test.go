package agent

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The live-mount helpers, against the fake Engine: what a helper is created
// with (Compose and Swarm), and what the sweep does to one whose session is
// gone. Each test names the bug it would have caught.

// envValue is one variable of a container's environment, "" when absent.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	return ""
}

// A Compose helper: the agent's image, the workload's volume at /mnt/vol, the
// session's ownership in its labels, ONE network (the first by name, so a
// re-provision picks the same), and a `hosts allow` carrying the agent's
// addresses. Would have caught: a helper joined to every network of the stack
// (beside every workload), a label set the sweep could not find it by, an
// allow list without the agent's own address (the mount refused the agent), a
// helper writing as another uid than the workload's.
func TestDockerMountVolumeCreatesTheHelperOnOneNetworkWithTheAgentAllowed(t *testing.T) {
	fe := newFakeEngine(t)
	t.Setenv(mountAllowEnv, "")
	fe.addNetwork("shop_default", "bridge", true)
	fe.addNetwork("shop_backend", "bridge", true)
	agent := fe.addContainer("shop-plug-1", true, nil, map[string][]string{"shop_default": {"plug"}, "shop_backend": {"plug"}})
	self := selfInfo{
		id: agent.ID, name: "shop-plug-1", image: "example/plug:test",
		nets: []netRef{
			{name: "shop_default", attachable: true, addrs: []string{"172.18.0.5"}},
			{name: "shop_backend", attachable: true, addrs: []string{"172.19.0.5"}},
		},
	}
	db := fe.addContainer("shop-db-1", true, nil, map[string][]string{"shop_default": {"db"}})
	fe.setContainerMounts(db.ID, dockerMount{Type: "volume", Name: "pgdata", Source: "/var/lib/docker/volumes/pgdata/_data", Destination: "/var/lib/postgresql/data"})
	fe.setContainerUser(db.ID, "70:70")
	fe.execAnswers("cat /proc/1/status", procStatus(999, 998))
	liveSessions(t)

	helper := mountHelperName("db", "pgdata", "41020")
	said := verbReply(t, func() { dockerMountVolume("db", "pgdata", "41020", hexPass, self) })
	if said != mountReply(helper) {
		t.Fatalf("mount answered %q, want %q", said, mountReply(helper))
	}
	c := fe.container(helper)
	if c == nil {
		t.Fatal("no helper container was created")
	}
	if !c.Running {
		t.Error("the helper was created but never started")
	}
	if c.User != "999:998" {
		t.Errorf("the helper runs as the workload's process does (999:998, read by exec), got User %q", c.User)
	}
	if note := envValue(c.Env, smbNoteEnv); note != "" {
		t.Errorf("ids read on the process need no note, got %q", note)
	}
	if k8sRequests(fe.requests(), "POST", "/containers/"+db.ID+"/exec") != 1 {
		t.Errorf("the ids are read in the workload's container, once: %v", fe.requests())
	}
	want := map[string]string{
		mountLabel: "1", mountOfLabel: "db", mountVolumeLabel: "pgdata",
		mountOwnerLabel: "shop-plug-1", sessionOwnerLabel: "shop-plug-1:41020",
	}
	for k, v := range want {
		if c.Labels[k] != v {
			t.Errorf("helper label %s = %q, want %q", k, c.Labels[k], v)
		}
	}
	if len(c.Networks) != 1 || c.Networks["shop_backend"] == nil {
		t.Errorf("the helper joins ONE network, the first by name (shop_backend), got %v", c.Networks)
	} else if a := c.Networks["shop_backend"].Aliases; len(a) != 1 || a[0] != helper {
		t.Errorf("the helper carries its own name as the alias the agent dials, got %v", a)
	}
	if len(c.HostMounts) != 1 || c.HostMounts[0]["Source"] != "pgdata" || c.HostMounts[0]["Target"] != mountVolumePath || c.HostMounts[0]["Type"] != "volume" {
		t.Errorf("the helper must mount the workload's volume at %s, got %v", mountVolumePath, c.HostMounts)
	}
	if strings.Join(c.Entrypoint, " ") != "/usr/local/bin/plug-agent mount-serve" || c.Image != "example/plug:test" {
		t.Errorf("the helper runs this image's mount-serve, got %v on %s", c.Entrypoint, c.Image)
	}
	allow := envValue(c.Env, smbAllowEnv)
	for _, addr := range []string{"172.18.0.5", "172.19.0.5"} {
		if !strings.Contains(allow, addr) {
			t.Errorf("%s=%q must carry the agent's address %s, or the helper refuses the agent", smbAllowEnv, allow, addr)
		}
	}
	if envValue(c.Env, smbPassEnv) != hexPass || envValue(c.Env, smbUserEnv) != mountUser || envValue(c.Env, smbShareEnv) != mountShare {
		t.Errorf("the helper's credential and share must be the session's: %v", c.Env)
	}
}

// The fallbacks on Docker, in order: a workload whose process cannot be read
// (parked, so stopped: no exec) gives the helper its declared user when that
// is numeric, and nothing when it is a name or absent, the image's own user
// then standing; the helper's log is told either way. Would have caught: a
// mount refused because the exec failed, a User "postgres" the helper's image
// cannot resolve, a User "0" invented.
func TestDockerMountVolumeFallsBackToTheDeclaredUserThenToNone(t *testing.T) {
	for _, tc := range []struct{ declared, want, note string }{
		{"1000:1000", "1000:1000", "declares"},
		{"1000", "1000", "declares"},
		{"postgres", "", "image's user"},
		{"", "", "image's user"},
	} {
		fe := newFakeEngine(t)
		t.Setenv(mountAllowEnv, "")
		fe.addNetwork("shop_default", "bridge", true)
		agent := fe.addContainer("shop-plug-1", true, nil, map[string][]string{"shop_default": {"plug"}})
		self := selfInfo{id: agent.ID, name: "shop-plug-1", image: "example/plug:test",
			nets: []netRef{{name: "shop_default", attachable: true, addrs: []string{"172.18.0.5"}}}}
		db := fe.addContainer("shop-db-1", true, nil, map[string][]string{"shop_default": {"db"}})
		fe.setContainerMounts(db.ID, dockerMount{Type: "volume", Name: "pgdata", Destination: "/var/lib/postgresql/data"})
		fe.setContainerUser(db.ID, tc.declared)
		// No exec scripted: the stream carries nothing, as from an image without cat.
		liveSessions(t)

		helper := mountHelperName("db", "pgdata", "41020")
		if said := verbReply(t, func() { dockerMountVolume("db", "pgdata", "41020", hexPass, self) }); said != mountReply(helper) {
			t.Fatalf("declared %q: mount answered %q", tc.declared, said)
		}
		c := fe.container(helper)
		if c == nil {
			t.Fatalf("declared %q: no helper", tc.declared)
		}
		if c.User != tc.want {
			t.Errorf("declared %q: helper User %q, want %q", tc.declared, c.User, tc.want)
		}
		if note := envValue(c.Env, smbNoteEnv); !strings.Contains(note, tc.note) {
			t.Errorf("declared %q: the helper's log must say where its uid comes from, got %q", tc.declared, note)
		}
	}
}

// procStatus is a /proc/1/status as the kernel writes it, for the two lines
// the agent reads among the others.
func procStatus(uid, gid int) string {
	return fmt.Sprintf("Name:\tpostgres\nUmask:\t0077\nState:\tS (sleeping)\nTgid:\t1\nPid:\t1\nPPid:\t0\n"+
		"Uid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nFDSize:\t64\nGroups:\t%d\nVmPeak:\t  218268 kB\n", uid, uid, uid, uid, gid, gid, gid, gid, gid)
}

// The sweep: a helper whose session no longer answers goes, one whose session
// answers stays, and on Swarm the same for helper services and their secrets.
// Would have caught: a sweep that reaped by owner role (every sibling's
// helpers), or left a dead session's secret in the store.
func TestDockerSweepMountHelpersRemovesDeadSessionsOnly(t *testing.T) {
	fe := newFakeEngine(t)
	live := liveSessions(t, "shop-plug-1:41020")
	dead := fe.addContainer("plug-mnt-db-dead0000", true, map[string]string{mountLabel: "1", mountOfLabel: "db", sessionOwnerLabel: "shop-plug-1:41019"}, nil)
	alive := fe.addContainer("plug-mnt-db-live0000", true, map[string]string{mountLabel: "1", mountOfLabel: "db", sessionOwnerLabel: "shop-plug-1:41020"}, nil)
	workload := fe.addContainer("shop-db-1", true, nil, nil)

	dockerSweepMountHelpers()
	if fe.container(dead.ID) != nil {
		t.Error("a helper whose session is gone must be removed")
	}
	if fe.container(alive.ID) == nil {
		t.Error("a helper whose session answers must stay")
	}
	if fe.container(workload.ID) == nil {
		t.Error("the sweep removed a container that is not a helper")
	}

	// Swarm: services and the secrets that carry their passwords.
	fe.setSwarm(true)
	deadSvc := fe.addService("plug-mnt-db-dead1111", map[string]string{mountLabel: "1", sessionOwnerLabel: "shop-plug-1:41019"}, 1, nil)
	liveSvc := fe.addService("plug-mnt-db-live1111", map[string]string{mountLabel: "1", sessionOwnerLabel: "shop-plug-1:41020"}, 1, nil)
	deadSecret := fe.addSecret("plug-mnt-db-dead1111", map[string]string{mountLabel: "1", sessionOwnerLabel: "shop-plug-1:41019"})
	liveSecret := fe.addSecret("plug-mnt-db-live1111", map[string]string{mountLabel: "1", sessionOwnerLabel: "shop-plug-1:41020"})
	dockerSweepMountHelpers()
	if fe.service(deadSvc.ID) != nil || fe.secret(deadSecret.ID) != nil {
		t.Error("a dead session's helper service and its secret must be removed")
	}
	if fe.service(liveSvc.ID) == nil || fe.secret(liveSecret.ID) == nil {
		t.Error("a live session's helper service and secret must stay")
	}

	// The live session ends: the next sweep takes its helper too.
	delete(live, "shop-plug-1:41020")
	dockerSweepMountHelpers()
	if fe.container(alive.ID) != nil || fe.service(liveSvc.ID) != nil || fe.secret(liveSecret.ID) != nil {
		t.Error("once its session stops answering, a helper is swept with its secret")
	}
}

// A Swarm helper: the password rides a secret (the service's environment names
// its file, never the value), the service is pinned to the node the workload's
// task runs on (a local volume lives on one node), joins one overlay under its
// own alias, and publishes NO virtual IP (dnsrr): through a VIP the load
// balancer rewrote the connection's source and the helper's allow list
// refused the agent itself. Would have caught: a service created with the
// default VIP endpoint, a password in `docker service inspect`, a helper
// scheduled where the volume is not, a helper writing as another uid than the
// workload's, a secret file the helper's uid cannot read.
func TestSwarmMountVolumeCreatesTheSecretAndADNSRRService(t *testing.T) {
	fe := newFakeEngine(t)
	t.Setenv(mountAllowEnv, "")
	self := swarmAgent(t, fe)
	db := fe.addService("shop_db", nil, 1, map[string][]string{"shop_net": {"db"}})
	fe.setServiceMounts(db.ID, dockerMount{Type: "volume", Source: "pgdata", Destination: "/var/lib/postgresql/data"})
	task := fe.addContainer("shop_db.1.task9", true, nil, nil)
	fe.addTask(db.ID, "node-b", "running", task.ID)
	fe.setServiceUser(db.ID, "70:70")
	fe.execAnswers("cat /proc/1/status", procStatus(999, 998))
	liveSessions(t)

	helper := mountHelperName("db", "pgdata", "41020")
	said := verbReply(t, func() { swarmMountVolume("db", "pgdata", "41020", hexPass, self) })
	if said != mountReply(helper) {
		t.Fatalf("mount answered %q, want %q", said, mountReply(helper))
	}
	secret := fe.secret(helper)
	if secret == nil {
		t.Fatal("no secret was created for the helper's password")
	}
	if data, _ := secret.Spec["Data"].(string); data != base64.StdEncoding.EncodeToString([]byte(hexPass)) {
		t.Errorf("the secret must carry the session's password, got %q", data)
	}
	if labels := specLabels(secret.Spec); labels[mountLabel] != "1" || labels[sessionOwnerLabel] != "shop_plug.1.task1:41020" {
		t.Errorf("the secret carries the helper's labels so the sweep finds it, got %v", labels)
	}
	sp, ok := fe.spec(helper)
	if !ok {
		t.Fatal("no helper service was created")
	}
	if sp.EndpointSpec.Mode != "dnsrr" {
		t.Errorf("the helper must publish no VIP (dnsrr), got endpoint mode %q", sp.EndpointSpec.Mode)
	}
	cs := sp.TaskTemplate.ContainerSpec
	if cs.User != "999:998" {
		t.Errorf("the helper runs as the workload's task does (999:998, read by exec), got User %q", cs.User)
	}
	if len(cs.Secrets) != 1 || cs.Secrets[0].SecretName != helper || cs.Secrets[0].File.Name != helper {
		t.Errorf("the service must mount its secret under the helper's name, got %+v", cs.Secrets)
	}
	// The secret's file is 0400: it has to belong to the uid that reads it.
	if cs.Secrets[0].File.UID != "999" || cs.Secrets[0].File.GID != "998" {
		t.Errorf("the secret's file must belong to the helper's uid, got %+v", cs.Secrets[0].File)
	}
	if envValue(cs.Env, smbNoteEnv) != "" {
		t.Errorf("nothing to note, got %q", envValue(cs.Env, smbNoteEnv))
	}
	if envValue(cs.Env, smbPassFileEnv) != secretsMount+"/"+helper || envValue(cs.Env, smbPassEnv) != "" {
		t.Errorf("the environment names the password's file and never carries the value, got %v", cs.Env)
	}
	if !strings.Contains(envValue(cs.Env, smbAllowEnv), "10.0.1.5") {
		t.Errorf("%s must carry the agent's overlay address, got %q", smbAllowEnv, envValue(cs.Env, smbAllowEnv))
	}
	if len(cs.Mounts) != 1 || cs.Mounts[0]["Source"] != "pgdata" || cs.Mounts[0]["Target"] != mountVolumePath {
		t.Errorf("the helper must mount the workload's volume at %s, got %v", mountVolumePath, cs.Mounts)
	}
	if len(sp.TaskTemplate.Networks) != 1 || sp.TaskTemplate.Networks[0].Target != "shop_net" ||
		strings.Join(sp.TaskTemplate.Networks[0].Aliases, ",") != helper {
		t.Errorf("one overlay, the helper's name as alias, got %+v", sp.TaskTemplate.Networks)
	}
	if strings.Join(sp.TaskTemplate.Placement.Constraints, ",") != "node.id==node-b" {
		t.Errorf("the helper must be placed on the node the workload's task runs on, got %v", sp.TaskTemplate.Placement.Constraints)
	}
	for k, v := range map[string]string{mountLabel: "1", mountOfLabel: "db", mountVolumeLabel: "pgdata", mountOwnerLabel: "shop_plug", sessionOwnerLabel: "shop_plug.1.task1:41020"} {
		if sp.Labels[k] != v {
			t.Errorf("helper label %s = %q, want %q", k, sp.Labels[k], v)
		}
	}
}

// The fallbacks on Swarm: a task this manager's socket does not reach (another
// node: no container of that id here) leaves the service's declared user, and
// a service that declares none leaves the helper to its image's user, with a
// secret file that is root's, since mount-serve reads the password before smbd
// changes identity. Would have caught: a multi-node mount refused for an exec
// that cannot be, a User "" sent to the daemon.
func TestSwarmMountVolumeFallsBackToTheDeclaredUserThenToNone(t *testing.T) {
	for _, tc := range []struct{ declared, want, fileUID, fileGID, note string }{
		{"1000:1001", "1000:1001", "1000", "1001", "declares"},
		{"", "", "0", "0", "image's user"},
	} {
		fe := newFakeEngine(t)
		t.Setenv(mountAllowEnv, "")
		self := swarmAgent(t, fe)
		db := fe.addService("shop_db", nil, 1, map[string][]string{"shop_net": {"db"}})
		fe.setServiceMounts(db.ID, dockerMount{Type: "volume", Source: "pgdata", Destination: "/var/lib/postgresql/data"})
		fe.addTask(db.ID, "node-b", "running", "a-container-on-another-node")
		if tc.declared != "" {
			fe.setServiceUser(db.ID, tc.declared)
		}
		fe.execAnswers("cat /proc/1/status", procStatus(999, 998)) // would answer, were the task here
		liveSessions(t)

		helper := mountHelperName("db", "pgdata", "41020")
		if said := verbReply(t, func() { swarmMountVolume("db", "pgdata", "41020", hexPass, self) }); said != mountReply(helper) {
			t.Fatalf("declared %q: mount answered %q", tc.declared, said)
		}
		sp, ok := fe.spec(helper)
		if !ok {
			t.Fatalf("declared %q: no helper service", tc.declared)
		}
		cs := sp.TaskTemplate.ContainerSpec
		if cs.User != tc.want {
			t.Errorf("declared %q: helper User %q, want %q", tc.declared, cs.User, tc.want)
		}
		if _, sent := anyMap(anyMap(fe.service(helper).Spec["TaskTemplate"])["ContainerSpec"])["User"]; sent != (tc.want != "") {
			t.Errorf("declared %q: User is sent only when there is one", tc.declared)
		}
		if len(cs.Secrets) != 1 || cs.Secrets[0].File.UID != tc.fileUID || cs.Secrets[0].File.GID != tc.fileGID {
			t.Errorf("declared %q: secret file owner %+v, want %s:%s", tc.declared, cs.Secrets, tc.fileUID, tc.fileGID)
		}
		if note := envValue(cs.Env, smbNoteEnv); !strings.Contains(note, tc.note) {
			t.Errorf("declared %q: the helper's log must say where its uid comes from, got %q", tc.declared, note)
		}
		if sp.EndpointSpec.Mode != "dnsrr" || strings.Join(sp.TaskTemplate.Placement.Constraints, ",") != "node.id==node-b" {
			t.Errorf("declared %q: the rest of the service does not depend on the ids: %+v", tc.declared, sp)
		}
	}
}

// seedK8sWorkload puts a workload behind a Service in the fake: the Service's
// selector, one Running pod on a node with a claim mounted in its SECOND
// container (the one whose process is read), the claim itself, and the agent's
// own Deployment (the image the helper runs is read off it). podSC and dbSC are
// the pod's and that container's securityContext, nil for none.
func seedK8sWorkload(fa *fakeAPIServer, podSC, dbSC map[string]any) {
	fa.add("services", k8sObject("Service", "db", nil, nil, map[string]any{
		"selector": map[string]string{"app": "db"},
		"ports":    []map[string]any{{"name": "pg", "port": 5432, "targetPort": 5432}},
	}))
	db := map[string]any{
		"name":         "db",
		"volumeMounts": []map[string]any{{"name": "data", "mountPath": "/var/lib/postgresql/data"}},
	}
	if dbSC != nil {
		db["securityContext"] = dbSC
	}
	spec := map[string]any{
		"nodeName":   "node-b",
		"volumes":    []map[string]any{{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "pgdata"}}},
		"containers": []map[string]any{{"name": "proxy"}, db},
	}
	if podSC != nil {
		spec["securityContext"] = podSC
	}
	fa.add("pods", k8sObject("Pod", "db-0", map[string]string{"app": "db"}, nil, spec))
	fa.add("persistentvolumeclaims", k8sObject("PersistentVolumeClaim", "pgdata", nil, nil, map[string]any{"accessModes": []string{"ReadWriteOnce"}}))
	fa.add("deployments", map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "plug", "labels": map[string]string{"app": "plug"}},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []map[string]any{{"name": "plug", "image": "example/plug:test"}},
		}}},
	})
}

// A Kubernetes helper, through the three ways its ids are settled. The exec
// answers: the pod runs as the workload's process does, whatever the spec
// declares, with the workload's fsGroup. The exec is refused (no pods/exec, an
// image without cat): the container's securityContext, then the pod's. Nothing
// anywhere: no uid named and the capabilities the helper needs to settle it
// itself, which a workload that is root keeps too. In every case 1445 in the container, 445 on the Service, 445 told to
// the client. Would have caught: an exec in the wrong container (the sidecar's
// uid), a pod the SCC refuses for a uid outside the range, a mount that fails
// where exec is not granted, a Service targeting a port nothing listens on.
func TestK8sMountVolumeRunsTheHelperAsTheWorkload(t *testing.T) {
	type sc = map[string]any
	for _, tc := range []struct {
		name          string
		exec          string // "" for an exec that is refused
		podSC, dbSC   sc
		wantPodSC     sc
		wantAdd, note bool
	}{
		{name: "exec answers", exec: procStatus(1000680000, 0),
			podSC: sc{"runAsUser": 5, "fsGroup": 1000680000}, dbSC: sc{"runAsUser": 6},
			wantPodSC: sc{"runAsUser": float64(1000680000), "runAsGroup": float64(0), "runAsNonRoot": true, "fsGroup": float64(1000680000)}},
		{name: "exec answers root", exec: procStatus(0, 0),
			wantPodSC: sc{"runAsUser": float64(0), "runAsGroup": float64(0)}, wantAdd: true},
		{name: "no exec, the container declares", podSC: sc{"runAsUser": 5, "runAsGroup": 7, "fsGroup": 9}, dbSC: sc{"runAsUser": 1001},
			wantPodSC: sc{"runAsUser": float64(1001), "runAsGroup": float64(7), "runAsNonRoot": true, "fsGroup": float64(9)}, note: true},
		{name: "no exec, the pod declares", podSC: sc{"runAsUser": 1002},
			wantPodSC: sc{"runAsUser": float64(1002), "runAsNonRoot": true}, note: true},
		{name: "no exec, nothing declared", wantAdd: true, note: true},
	} {
		fa := newFakeAPIServer(t, testNS, testPodIP)
		t.Setenv(mountAllowEnv, "")
		seedK8sWorkload(fa, tc.podSC, tc.dbSC)
		if tc.exec != "" {
			fa.execAnswers("cat /proc/1/status", tc.exec)
		}

		helper := mountHelperName("db", "pgdata", "41020")
		if said := verbReply(t, func() { k8sMountVolume(testNS, "db", "pgdata", "41020", hexPass) }); said != mountReply(helper) {
			t.Fatalf("%s: mount answered %q, want %q", tc.name, said, mountReply(helper))
		}
		execs := 0
		for _, r := range fa.requests() {
			if strings.HasPrefix(r, "GET /api/v1/namespaces/"+testNS+"/pods/db-0/exec?") {
				execs++
				if !strings.Contains(r, "container=db&") && !strings.HasSuffix(r, "container=db") {
					t.Errorf("%s: the ids are read in the container that mounts the volume, got %s", tc.name, r)
				}
			}
			if strings.Contains(r, "security.openshift.io") {
				t.Errorf("%s: the platform is not asked what it is: %s", tc.name, r)
			}
		}
		if execs != 1 {
			t.Errorf("%s: one exec in the workload's pod, got %d in %v", tc.name, execs, fa.requests())
		}
		pod := fa.get("pods", helper)
		if pod == nil {
			t.Fatalf("%s: no helper pod", tc.name)
		}
		spec := anyMap(pod["spec"])
		if got := anyMap(spec["securityContext"]); !reflect.DeepEqual(got, tc.wantPodSC) && (len(got) != 0 || len(tc.wantPodSC) != 0) {
			t.Errorf("%s: pod securityContext %v, want %v", tc.name, got, tc.wantPodSC)
		}
		c := anyMap(spec["containers"].([]any)[0])
		if port := anyMap(c["ports"].([]any)[0])["containerPort"]; port != float64(1445) {
			t.Errorf("%s: containerPort %v, want 1445", tc.name, port)
		}
		csc := anyMap(c["securityContext"])
		caps := anyMap(csc["capabilities"])
		if csc["allowPrivilegeEscalation"] != false || !reflect.DeepEqual(caps["drop"], []any{"ALL"}) || anyMap(csc["seccompProfile"])["type"] != "RuntimeDefault" {
			t.Errorf("%s: container securityContext %v", tc.name, csc)
		}
		if _, added := caps["add"]; added != tc.wantAdd {
			t.Errorf("%s: capabilities %v, added want %v", tc.name, caps, tc.wantAdd)
		}
		env := map[string]string{}
		for _, e := range c["env"].([]any) {
			env[anyMap(e)["name"].(string)], _ = anyMap(e)["value"].(string)
		}
		if _, noted := env[smbNoteEnv]; noted != tc.note {
			t.Errorf("%s: note %q, want one: %v", tc.name, env[smbNoteEnv], tc.note)
		}
		if spec["nodeName"] != "node-b" || env[smbPassEnv] != hexPass {
			t.Errorf("%s: pinned to the workload's node with the session's credential, got %v / %v", tc.name, spec["nodeName"], env)
		}
		svc, ok := fa.service(helper)
		if !ok || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 445 || svc.Spec.Ports[0].TargetPort != float64(1445) {
			t.Errorf("%s: the Service answers 445 and targets 1445, got %+v", tc.name, svc.Spec.Ports)
		}
		if svc.Spec.Selector[mountHelperLabel] != helper {
			t.Errorf("%s: the Service selects the helper, got %v", tc.name, svc.Spec.Selector)
		}
	}
}
