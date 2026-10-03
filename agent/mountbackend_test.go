package agent

import (
	"encoding/base64"
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
// allow list without the agent's own address (the mount refused the agent).
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
// scheduled where the volume is not.
func TestSwarmMountVolumeCreatesTheSecretAndADNSRRService(t *testing.T) {
	fe := newFakeEngine(t)
	t.Setenv(mountAllowEnv, "")
	self := swarmAgent(t, fe)
	db := fe.addService("shop_db", nil, 1, map[string][]string{"shop_net": {"db"}})
	fe.setServiceMounts(db.ID, dockerMount{Type: "volume", Source: "pgdata", Destination: "/var/lib/postgresql/data"})
	fe.addTask(db.ID, "node-b", "running", "")
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
	if len(cs.Secrets) != 1 || cs.Secrets[0].SecretName != helper || cs.Secrets[0].File.Name != helper {
		t.Errorf("the service must mount its secret under the helper's name, got %+v", cs.Secrets)
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
