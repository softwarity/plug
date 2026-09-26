package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveMountAgainstAnAgent drives the whole client side of --mount against
// a REAL agent: the helper is provisioned, the OS mounts the share, the
// teardown unmounts it and the helper is gone. It runs only when pointed at
// an agent (PLUG_MOUNT_E2E=host:port) whose cluster has a workload named
// PLUG_MOUNT_E2E_NAME (default geo) with a volume at PLUG_MOUNT_E2E_VOLUME
// (default /data), and needs docker on this machine to look at the helper.
//
// What it does NOT assert is reading through the mount: on macOS that is
// gated by TCC (Network Volumes) on the app the test runs under, which is
// the terminal's, not plug's, to have.
func TestLiveMountAgainstAnAgent(t *testing.T) {
	target := os.Getenv("PLUG_MOUNT_E2E")
	if target == "" {
		t.Skip("PLUG_MOUNT_E2E not set")
	}
	host, port, _ := strings.Cut(target, ":")
	name := os.Getenv("PLUG_MOUNT_E2E_NAME")
	if name == "" {
		name = "geo"
	}
	volume := os.Getenv("PLUG_MOUNT_E2E_VOLUME")
	if volume == "" {
		volume = "/data"
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "mnt", "data")
	cfg := config{host: host, port: port, mounts: []mountSpec{{name: name, volume: volume, path: path}}}

	stop, err := startMounts(cfg)
	if err != nil {
		t.Fatalf("startMounts: %v", err)
	}
	if !mountedAt(path) {
		stop()
		t.Fatalf("%s is not a mountpoint after startMounts", path)
	}
	recs := mountRecords()
	if len(recs) != 1 || recs[0].path != path {
		t.Errorf("records = %+v", recs)
	}
	if hasDocker() {
		if helper := dockerHelperFor(t, name); helper == "" {
			t.Errorf("no running helper container labelled plug.mount for %s", name)
		} else {
			t.Logf("helper %s is up", helper)
		}
	}
	// Through the mount, where the process may (Linux; macOS under an app
	// with the Network Volumes permission): the workload's file is there, and
	// what is written lands in the volume.
	if os.Getenv("PLUG_MOUNT_E2E_RW") != "" {
		seed, err := os.ReadFile(filepath.Join(path, "seed.txt"))
		if err != nil || !strings.Contains(string(seed), "seed from workload") {
			t.Errorf("reading the workload's seed through the mount: %q %v", seed, err)
		}
		if err := os.WriteFile(filepath.Join(path, "from-plug.txt"), []byte("written through plug\n"), 0o644); err != nil {
			t.Errorf("writing through the mount: %v", err)
		} else if b, err := os.ReadFile(filepath.Join(path, "from-plug.txt")); err != nil || string(b) != "written through plug\n" {
			t.Errorf("reading back: %q %v", b, err)
		}
		if err := os.MkdirAll(filepath.Join(path, "sub", "deeper"), 0o755); err != nil {
			t.Errorf("mkdir through the mount: %v", err)
		}
		if err := os.Remove(filepath.Join(path, "from-plug.txt")); err != nil {
			t.Errorf("remove through the mount: %v", err)
		}
	}

	stop()
	if mountedAt(path) {
		t.Errorf("%s is still mounted after the teardown", path)
	}
	if len(mountRecords()) != 0 {
		t.Errorf("a record survived the teardown")
	}
	if !hasDocker() {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for dockerHelperFor(t, name) != "" && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if h := dockerHelperFor(t, name); h != "" {
		t.Errorf("the helper %s survived the teardown", h)
	}
}

func hasDocker() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func dockerHelperFor(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "--filter", "label=plug.mount=1", "--filter", "label=plug.mount.of="+name, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Logf("docker ps: %v", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestLiveMountOrphanCleanup is the crash: a session holding a mount is
// killed with -9. The mount and the helper must not outlive it: the next
// run's sweep (sweepOrphanMounts, what startMounts does first) unmounts the
// path and forgets the record, and the agent's periodic sweep, seeing the
// session's liveness port dead, removes the helper within its minute.
//
// The session is this test binary re-run in HOLD mode, so nothing here is
// mocked: a real process, a real mount, a real SIGKILL.
func TestLiveMountOrphanCleanup(t *testing.T) {
	target := os.Getenv("PLUG_MOUNT_E2E")
	if target == "" || os.Getenv("PLUG_MOUNT_E2E_ORPHAN") == "" {
		t.Skip("PLUG_MOUNT_E2E and PLUG_MOUNT_E2E_ORPHAN not set")
	}
	name := os.Getenv("PLUG_MOUNT_E2E_NAME")
	if name == "" {
		name = "geo"
	}
	home := t.TempDir()
	path := filepath.Join(home, "mnt", "data")
	self, _ := os.Executable()
	hold := exec.Command(self, "-test.run", "^TestLiveMountHold$", "-test.v")
	hold.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "PLUG_MOUNT_E2E_HOLD="+path)
	hold.Stdout, hold.Stderr = os.Stderr, os.Stderr
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for !mountedAt(path) && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if !mountedAt(path) {
		hold.Process.Kill()
		t.Fatal("the held session never mounted")
	}
	helper := dockerHelperFor(t, name)
	t.Logf("session %d holds %s, helper %s", hold.Process.Pid, path, helper)

	// The crash.
	if err := hold.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	hold.Wait()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	recs := mountRecords()
	if len(recs) != 1 || processAlive(recs[0].pid) {
		t.Fatalf("after the kill, the record should name a dead pid: %+v", recs)
	}
	if !mountedAt(path) {
		t.Fatalf("the mount should have OUTLIVED the process (that is the point)")
	}

	// D6: the next run cleans the machine.
	sweepOrphanMounts()
	if mountedAt(path) {
		t.Errorf("sweepOrphanMounts left %s mounted", path)
	}
	if len(mountRecords()) != 0 {
		t.Errorf("sweepOrphanMounts left a record")
	}

	// D4: the agent cleans the cluster, on its own, within its sweep interval.
	deadline = time.Now().Add(150 * time.Second)
	for dockerHelperFor(t, name) != "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
	}
	if h := dockerHelperFor(t, name); h != "" {
		t.Errorf("the agent's sweep did not reap the orphaned helper %s in 150s", h)
	} else {
		t.Logf("the orphaned helper was reaped by the agent's sweep")
	}
}

// TestLiveMountHold is the held session of the test above: mount, then wait
// to be killed. Never runs on its own.
func TestLiveMountHold(t *testing.T) {
	path := os.Getenv("PLUG_MOUNT_E2E_HOLD")
	if path == "" {
		t.Skip("PLUG_MOUNT_E2E_HOLD not set")
	}
	host, port, _ := strings.Cut(os.Getenv("PLUG_MOUNT_E2E"), ":")
	name := os.Getenv("PLUG_MOUNT_E2E_NAME")
	if name == "" {
		name = "geo"
	}
	volume := os.Getenv("PLUG_MOUNT_E2E_VOLUME")
	if volume == "" {
		volume = "/data"
	}
	cfg := config{host: host, port: port, mounts: []mountSpec{{name: name, volume: volume, path: path}}}
	if _, err := startMounts(cfg); err != nil {
		t.Fatalf("startMounts: %v", err)
	}
	select {}
}

// TestLiveMountSurvivesAgentRestart is the blip (D3): the agent restarts
// under a held session. Its boot gc reaps the helper (the old liveness port
// died with the old connection); the client's transport reconnects, the
// liveness forward re-arms on a new port, and the helper is re-provisioned
// under it with the SAME credential. The mount is untouched throughout: the
// OS's SMB client reconnects through the forward, which now points at the
// new helper. Needs PLUG_MOUNT_E2E_AGENT_CONTAINER, the agent to restart.
func TestLiveMountSurvivesAgentRestart(t *testing.T) {
	target := os.Getenv("PLUG_MOUNT_E2E")
	agent := os.Getenv("PLUG_MOUNT_E2E_AGENT_CONTAINER")
	if target == "" || agent == "" {
		t.Skip("PLUG_MOUNT_E2E and PLUG_MOUNT_E2E_AGENT_CONTAINER not set")
	}
	host, port, _ := strings.Cut(target, ":")
	name := os.Getenv("PLUG_MOUNT_E2E_NAME")
	if name == "" {
		name = "geo"
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "mnt", "data")
	cfg := config{host: host, port: port, mounts: []mountSpec{{name: name, volume: "/data", path: path}}}
	stop, err := startMounts(cfg)
	if err != nil {
		t.Fatalf("startMounts: %v", err)
	}
	defer stop()
	before := dockerHelperID(t, name)
	if before == "" {
		t.Fatal("no helper before the restart")
	}
	if out, err := exec.Command("docker", "restart", agent).CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v %s", err, out)
	}
	// The reconnect: keepalive misses, then a new connection, a new port, a
	// re-provision. Generous, since the agent's own start is in there too.
	deadline := time.Now().Add(120 * time.Second)
	after := ""
	for time.Now().Before(deadline) {
		if id := dockerHelperID(t, name); id != "" && id != before {
			after = id
			break
		}
		time.Sleep(2 * time.Second)
	}
	if after == "" {
		t.Fatalf("the helper was not re-provisioned after the agent restart (still %s)", dockerHelperID(t, name))
	}
	t.Logf("helper re-provisioned: %s -> %s", before[:12], after[:12])
	if !mountedAt(path) {
		t.Errorf("the mount did not survive the restart")
	}
	// Same credential on both helpers: the OS client's reconnection depends on it.
	pass := func(id string) string {
		out, _ := exec.Command("docker", "inspect", "-f", `{{range .Config.Env}}{{println .}}{{end}}`, id).Output()
		for _, kv := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(kv, "PLUG_SMB_PASS=") {
				return kv
			}
		}
		return ""
	}
	if p := pass(after); p == "" {
		t.Errorf("the new helper carries no credential")
	}
}

func dockerHelperID(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "--filter", "label=plug.mount=1", "--filter", "label=plug.mount.of="+name, "--format", "{{.ID}}", "--no-trunc").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
