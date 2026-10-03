package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The verb's argument gate: what reaches an orchestrator API, and what does
// not. A volume is a name or an absolute path; a name is not a shell word.
func TestVolumeArgShape(t *testing.T) {
	ok := []string{"data", "pg_data", "my-vol.v2", "/data", "/var/lib/postgresql/data", "A1"}
	for _, v := range ok {
		if !volumeArgOK(v) {
			t.Errorf("%q should be accepted", v)
		}
	}
	bad := []string{"", "-data", ".hidden", "/", "/a/../b", "a b", "/a b", "a;rm", "a/b",
		strings.Repeat("x", 64), "/" + strings.Repeat("x", 300)}
	for _, v := range bad {
		if volumeArgOK(v) {
			t.Errorf("%q should be refused", v)
		}
	}
}

// dispatch refuses a malformed mount-volume before any backend is asked. The
// answer is captured the way dispatch_test does: answer is a var.
const hexPass = "0123456789abcdef0123456789abcdef01234567"

func TestMountVerbsValidateBeforeActing(t *testing.T) {
	var got string
	old := answer
	answer = func(format string, a ...any) { got = fmt.Sprintf(format, a...); panic("answered") }
	defer func() { answer = old }()
	try := func(cmd ...string) string {
		got = ""
		func() {
			defer func() { recover() }()
			dispatch(cmd)
		}()
		return got
	}
	for _, c := range [][]string{
		{"mount-volume"},
		{"mount-volume", "web"},
		{"mount-volume", "web", "data"},
		{"mount-volume", "web", "data", "40000"},
		{"mount-volume", "Web", "data", "40000", hexPass},
		{"mount-volume", "web", "a b", "40000", hexPass},
		{"mount-volume", "web", "data", "0", hexPass},
		{"mount-volume", "web", "data", "70000", hexPass},
		{"mount-volume", "web", "data", "x", hexPass},
		{"mount-volume", "web", "data", "40000", "short"},
		{"mount-volume", "web", "data", "40000", strings.Repeat("g", 40)},
		{"mount-volume", "web", "data", "40000", hexPass, "extra"},
		{"unmount-volume"},
		{"unmount-volume", "web", "data"},
		{"unmount-volume", "web", "data", "40000", "extra"},
		{"unmount-volume", "web", "/a/../b", "40000"},
		{"unmount-volume", "web", "data", "x"},
	} {
		if r := try(c...); !strings.HasPrefix(r, "error: ") {
			t.Errorf("%v: expected a refusal, got %q", c, r)
		}
	}
}

// Helper names are deterministic per (workload, volume, session), a valid
// container and DNS name, and distinct across volumes of one workload and
// across sessions on one volume.
func TestMountHelperName(t *testing.T) {
	a := mountHelperName("web", "data", "40001")
	if a != mountHelperName("web", "data", "40001") {
		t.Fatal("the helper name must be deterministic")
	}
	if a == mountHelperName("web", "/data", "40001") || a == mountHelperName("api", "data", "40001") || a == mountHelperName("web", "data", "40002") {
		t.Fatal("distinct (workload, volume, session) triples must not share a helper")
	}
	if !strings.HasPrefix(a, "plug-mnt-web-") || len(a) != len("plug-mnt-web-")+8 {
		t.Fatalf("unexpected shape %q", a)
	}
	if !nameRe.MatchString(a) {
		t.Fatalf("%q is not a DNS label", a)
	}
	// The workload's name is a label of up to 63 characters, and the helper's
	// name wraps it in a prefix and a hash: past 45 the whole no longer fits a
	// label, and every backend refused the helper (the k8s label value, the k8s
	// Service name, the Swarm service name). A name of 45 is the last one to
	// fit, and it keeps the shape every session knows.
	n45 := strings.Repeat("w", 45)
	if got := mountHelperName(n45, "data", "40001"); len(got) != 63 || !strings.HasPrefix(got, "plug-mnt-"+n45+"-") {
		t.Fatalf("a 45-char name fits as it always did, got %q (%d)", got, len(got))
	}
	for _, n := range []string{strings.Repeat("w", 46), strings.Repeat("w", 63), "a" + strings.Repeat("-b", 31)} {
		got := mountHelperName(n, "data", "40001")
		if len(got) > clusterNameMax || !nameRe.MatchString(got) {
			t.Fatalf("mountHelperName(%d chars) = %q: not a label the cluster takes", len(n), got)
		}
		if !strings.HasPrefix(got, "plug-mnt-") {
			t.Fatalf("the prefix is how a helper is told apart, got %q", got)
		}
		if got == mountHelperName(n, "logs", "40001") || got == mountHelperName(n, "data", "40002") {
			t.Fatalf("a cut name must still tell volumes and sessions apart: %q", got)
		}
		if got != mountHelperName(n, "data", "40001") {
			t.Fatal("a cut name must still be deterministic")
		}
	}
}

// The reply line is one token per field, so the client splits it on spaces;
// the password is the client's own and never comes back.
func TestMountReplyShape(t *testing.T) {
	if !passArgOK(hexPass) || passArgOK(strings.ToUpper(hexPass)) || passArgOK(hexPass[:31]) || passArgOK(strings.Repeat("a", 65)) {
		t.Fatal("passArgOK")
	}
	line := mountReply("10.0.0.7")
	f := strings.Fields(line)
	if len(f) != 5 || f[0] != "mounted" {
		t.Fatalf("reply = %q", line)
	}
	want := map[string]string{"host": "10.0.0.7", "port": "445", "share": "vol", "user": mountUser}
	for _, kv := range f[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || want[k] != v {
			t.Errorf("field %q: want %s=%s", kv, k, want[k])
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("missing fields: %v", want)
	}
}

// What the developer asks for is resolved against what the container mounts:
// by path inside the container, or by volume name; a bind by its path; a
// tmpfs never. The refusal lists what was there.
func TestPickDockerMount(t *testing.T) {
	mounts := []dockerMount{
		{Type: "volume", Name: "pg_data", Source: "/var/lib/docker/volumes/pg_data/_data", Destination: "/var/lib/postgresql/data"},
		{Type: "bind", Source: "/srv/conf", Destination: "/etc/app"},
		{Type: "tmpfs", Destination: "/tmp"},
	}
	if m, _ := pickDockerMount("pg_data", mounts); m == nil || m.Name != "pg_data" {
		t.Fatal("by volume name")
	}
	if m, _ := pickDockerMount("/var/lib/postgresql/data", mounts); m == nil || m.Name != "pg_data" {
		t.Fatal("by container path")
	}
	if m, _ := pickDockerMount("/etc/app", mounts); m == nil || m.Type != "bind" || m.Source != "/srv/conf" {
		t.Fatal("a bind, by its path")
	}
	if m, _ := pickDockerMount("/tmp", mounts); m != nil {
		t.Fatal("a tmpfs has nothing to serve")
	}
	m, why := pickDockerMount("nope", mounts)
	if m != nil || !strings.Contains(why, "/var/lib/postgresql/data (volume pg_data)") || !strings.Contains(why, "/etc/app (bind /srv/conf)") || strings.Contains(why, "/tmp") {
		t.Fatalf("refusal should list the real mounts and not the tmpfs: %q", why)
	}
	if _, why := pickDockerMount("x", nil); why != "it mounts nothing" {
		t.Fatalf("no mounts: %q", why)
	}
	// The mount spec puts a volume by NAME and a bind by SOURCE at /mnt/vol.
	if s := mountSpec(&mounts[0]); s["Type"] != "volume" || s["Source"] != "pg_data" || s["Target"] != mountVolumePath {
		t.Fatalf("volume spec %v", s)
	}
	if s := mountSpec(&mounts[1]); s["Type"] != "bind" || s["Source"] != "/srv/conf" {
		t.Fatalf("bind spec %v", s)
	}
}

// A bind hands the helper a directory of the HOST, read-write and as root
// when root owns it: the host's root, its configuration, its device nodes and
// the daemon's own storage are refused, a project directory is not.
func TestBindSourceRefused(t *testing.T) {
	for _, src := range []string{
		"/", "/.", "//", "/etc", "/etc/", "/etc/nginx", "/var/run", "/var/run/docker.sock", "/run", "/run/secrets",
		"/proc", "/sys", "/dev", "/boot", "/root", "/root/.ssh", "/usr", "/usr/local/bin", "/bin", "/sbin",
		"/lib", "/lib64", "/var/lib/docker", "/var/lib/docker/volumes/x/_data",
		"/srv/../etc/nginx", // cleaned before it is compared
		"relative/path", "",
	} {
		if why := bindSourceRefused(src); why == "" {
			t.Errorf("%q must be refused", src)
		}
	}
	for _, src := range []string{
		"/srv/conf", "/home/dev/project", "/opt/app/data", "/data", "/var/lib/postgresql", "/var/www",
		"/mnt/data", "/tmp/build", "/etcetera", "/libraries", "/host_mnt/c/Users/dev/project", "/Users/dev/project",
	} {
		if why := bindSourceRefused(src); why != "" {
			t.Errorf("%q is a project directory and must be served, got %q", src, why)
		}
	}
	// mountRefused applies it to binds only: a named volume is the daemon's own.
	if why := mountRefused(&dockerMount{Type: "volume", Name: "etc", Source: "/var/lib/docker/volumes/etc/_data"}); why != "" {
		t.Errorf("a named volume is never refused, got %q", why)
	}
	if why := mountRefused(&dockerMount{Type: "bind", Source: "/etc", Destination: "/host-etc"}); why == "" || !strings.Contains(why, "/etc") {
		t.Errorf("a bind of /etc must be refused and named, got %q", why)
	}
	if why := mountRefused(&dockerMount{Type: "bind", Source: "/srv/conf", Destination: "/etc/app"}); why != "" {
		t.Errorf("the destination inside the container does not matter, got %q", why)
	}
}

// The Kubernetes half of the same resolution: a PVC by mount path or by claim
// name, and only PVC-backed mounts count (a configMap volume is files-of's).
func TestPickClaim(t *testing.T) {
	claim := func(n string) *struct {
		ClaimName string `json:"claimName"`
	} {
		return &struct {
			ClaimName string `json:"claimName"`
		}{ClaimName: n}
	}
	vols := []k8sClaimVolume{
		{Name: "data", PVC: claim("geoserver-data")},
		{Name: "cfg"},
	}
	mounts := []k8sMount{
		{Name: "data", MountPath: "/opt/geoserver/data_dir"},
		{Name: "cfg", MountPath: "/etc/cfg"},
	}
	if c, _ := pickClaim("geoserver-data", vols, mounts); c != "geoserver-data" {
		t.Fatal("by claim name")
	}
	if c, _ := pickClaim("/opt/geoserver/data_dir", vols, mounts); c != "geoserver-data" {
		t.Fatal("by mount path")
	}
	if c, why := pickClaim("/etc/cfg", vols, mounts); c != "" || !strings.Contains(why, "/opt/geoserver/data_dir (pvc geoserver-data)") || strings.Contains(why, "/etc/cfg") {
		t.Fatalf("a configMap mount is not a claim: %q", why)
	}
	if _, why := pickClaim("x", nil, mounts); why != "it mounts no PersistentVolumeClaim" {
		t.Fatalf("no claims: %q", why)
	}
}

// The helper pod: the claim at /mnt/vol, the credential in the environment,
// pinned to the workload's node, the session owner in an ANNOTATION (a label
// value cannot carry host:port) and a label-safe volume value (a path cannot
// be a label value either).
func TestK8sMountPodShape(t *testing.T) {
	pod := k8sMountPod("shop", "plug-mnt-web-abcd1234", "web", "/data", "web-data", "node-2",
		"softwarity/plug:2.20.0", "10.1.2.3:40001", "s3cret", []string{"10.1.2.3"})
	meta := pod["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]string)
	ann := meta["annotations"].(map[string]string)
	if labels[mountLabel] != "1" || labels[mountOfLabel] != "web" || labels["app"] != "plug-mount" {
		t.Fatalf("labels %v", labels)
	}
	if strings.Contains(labels[mountVolumeLabel], "/") || ann[mountVolumeLabel] != "/data" {
		t.Fatalf("volume: label %q, annotation %q", labels[mountVolumeLabel], ann[mountVolumeLabel])
	}
	if ann[sessionOwnerLabel] != "10.1.2.3:40001" {
		t.Fatalf("owner annotation %q", ann[sessionOwnerLabel])
	}
	spec := pod["spec"].(map[string]any)
	if spec["nodeName"] != "node-2" {
		t.Fatalf("nodeName %v", spec["nodeName"])
	}
	vols := spec["volumes"].([]map[string]any)
	if len(vols) != 1 || vols[0]["persistentVolumeClaim"].(map[string]any)["claimName"] != "web-data" {
		t.Fatalf("volumes %v", vols)
	}
	c := spec["containers"].([]map[string]any)[0]
	if c["image"] != "softwarity/plug:2.20.0" {
		t.Fatalf("image %v", c["image"])
	}
	if cmd := c["command"].([]string); len(cmd) != 2 || cmd[1] != "mount-serve" {
		t.Fatalf("command %v", cmd)
	}
	if vm := c["volumeMounts"].([]map[string]any); vm[0]["mountPath"] != mountVolumePath {
		t.Fatalf("volumeMounts %v", vm)
	}
	env := map[string]string{}
	for _, e := range c["env"].([]map[string]string) {
		env[e["name"]] = e["value"]
	}
	if env[smbUserEnv] != "plug" || env[smbPassEnv] != "s3cret" || env[smbShareEnv] != mountShare || env[smbAllowEnv] != "10.1.2.3" {
		t.Fatalf("env %v", env)
	}
	// On Kubernetes the password stays in the environment: a Secret would
	// need a verb the manifest's RBAC does not grant.
	if _, viaFile := env[smbPassFileEnv]; viaFile {
		t.Fatal("the k8s helper must not be pointed at a password file nothing mounts")
	}
	// No node known: nothing pinned, the scheduler decides.
	free := k8sMountPod("shop", "h", "web", "data", "c", "", "img", "o", "p", nil)
	if _, pinned := free["spec"].(map[string]any)["nodeName"]; pinned {
		t.Fatal("an unknown node must not be pinned to \"\"")
	}
	if labelSafe("web-data") != "web-data" || !strings.HasPrefix(labelSafe("/a/b"), "path-") {
		t.Fatal("labelSafe")
	}
}

// The helper's own side (mountserve.go): the Samba configuration and the
// account file it writes.
func TestSmbConf(t *testing.T) {
	c := smbConf("vol", "plug", "plug", true, []string{"10.0.1.5", "fd00::5"})
	for _, want := range []string{
		"[global]", "security = user", "passdb backend = smbpasswd", "server min protocol = SMB2_02",
		"smb ports = 445", "disable netbios = yes", "load printers = no",
		"vfs objects = fruit streams_xattr",
		// Only the agent may connect: signing every client does unasked,
		// encryption when the client has it and never as a condition.
		"hosts allow = 10.0.1.5 fd00::5\n", "server signing = mandatory", "smb encrypt = desired",
		"[vol]", "path = " + mountVolumePath, "read only = no", "valid users = plug", "force user = plug",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	if strings.Contains(c, "smb encrypt = required") || strings.Contains(c, "hosts deny") {
		t.Error("a client that does not encrypt must still mount; the allow list alone is the filter")
	}
	// The filter lives in [global], before the share, so it covers the
	// negotiation itself and not just the tree connect.
	if strings.Index(c, "hosts allow") > strings.Index(c, "[vol]") {
		t.Error("hosts allow must be global")
	}
	plain := smbConf("vol", "plug", "root", false, nil)
	if strings.Contains(plain, "fruit") {
		t.Error("no fruit module, no fruit configuration")
	}
	if !strings.Contains(plain, "force user = root") {
		t.Error("a root-owned volume is served as root")
	}
	// An agent that could not tell its addresses sets no filter at all: a
	// refusal would be a mount lost for a guess.
	if strings.Contains(plain, "hosts allow") {
		t.Error("an empty allow list must not restrict anything")
	}
}

// The password reaches the helper by one of two routes: the environment
// (Docker, Kubernetes) or a file the orchestrator mounts (a Swarm secret),
// trimmed of the newline a secret made with echo carries.
func TestSmbPassword(t *testing.T) {
	if p, from, err := smbPassword("abc", ""); p != "abc" || err != nil || !strings.Contains(from, "environment") {
		t.Fatalf("env: %q %q %v", p, from, err)
	}
	f := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(f, []byte("fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, from, err := smbPassword("", f); p != "fromfile" || err != nil || !strings.Contains(from, f) {
		t.Fatalf("file: %q %q %v", p, from, err)
	}
	// The environment wins when both are set: it is what the agent meant.
	if p, _, _ := smbPassword("env", f); p != "env" {
		t.Fatalf("both: %q", p)
	}
	if p, _, err := smbPassword("", ""); p != "" || err != nil {
		t.Fatalf("neither: %q %v", p, err)
	}
	if _, _, err := smbPassword("", filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing password file is an error, not an empty password")
	}
}

// mountEnv, the agent's side of the same contract: the password in the
// environment OR the file's path, never both; the allow list always, empty
// when unknown; the note only when there is one.
func TestMountEnv(t *testing.T) {
	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	env := mountEnv("s3cret", "", []string{"10.0.1.5", "10.0.2.5"}, "")
	if !has(env, smbPassEnv+"=s3cret") || !has(env, smbAllowEnv+"=10.0.1.5 10.0.2.5") || !has(env, smbUserEnv+"="+mountUser) || !has(env, smbShareEnv+"="+mountShare) {
		t.Fatalf("env %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, smbPassFileEnv+"=") || strings.HasPrefix(e, smbNoteEnv+"=") {
			t.Fatalf("unasked variable in %v", env)
		}
	}
	viaFile := mountEnv("", "/run/secrets/plug-mnt-web-abcd1234", nil, "fell back")
	if !has(viaFile, smbPassFileEnv+"=/run/secrets/plug-mnt-web-abcd1234") || !has(viaFile, smbAllowEnv+"=") || !has(viaFile, smbNoteEnv+"=fell back") {
		t.Fatalf("env %v", viaFile)
	}
	for _, e := range viaFile {
		if strings.HasPrefix(e, smbPassEnv+"=") {
			t.Fatalf("the password must not ride the environment beside its file: %v", viaFile)
		}
	}
}

// mountAllow is built from this process's interfaces plus what the
// orchestrator adds; loopback never counts, a duplicate counts once, an
// operator's "any" clears it all, and an operator's subnet rides along.
func TestMountAllow(t *testing.T) {
	t.Setenv(mountAllowEnv, "")
	got := mountAllow("10.9.9.9", "127.0.0.1", "10.9.9.9", "", "fe80::1")
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a] {
			t.Fatalf("duplicate %s in %v", a, got)
		}
		seen[a] = true
		if ip := net.ParseIP(a); ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("%s has no place in an allow list: %v", a, got)
		}
	}
	if !seen["10.9.9.9"] {
		t.Fatalf("the orchestrator's address is missing from %v", got)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("not deterministic: %v", got)
	}
	t.Setenv(mountAllowEnv, "10.244.0.0/16 any")
	if got := mountAllow("10.9.9.9"); got != nil {
		t.Fatalf("\"any\" must clear the filter, got %v", got)
	}
	t.Setenv(mountAllowEnv, "10.244.0.0/16")
	got = mountAllow("10.9.9.9")
	found := false
	for _, a := range got {
		found = found || a == "10.244.0.0/16"
	}
	if !found {
		t.Fatalf("the operator's subnet is missing from %v", got)
	}
}

// The helper joins ONE network, the same one at every re-provision whatever
// order the daemon listed them in.
func TestMountNetwork(t *testing.T) {
	if mountNetwork([]string{"shop_default", "shop_backend"}) != "shop_backend" || mountNetwork([]string{"shop_backend", "shop_default"}) != "shop_backend" {
		t.Fatal("the choice must not depend on the order")
	}
	if mountNetwork([]string{"only"}) != "only" {
		t.Fatal("one network is that network")
	}
}

// The NT hash is MD4 over UTF-16LE: the known vector for "password", and the
// smbpasswd(5) line around it.
func TestNTHashAndSmbPasswdLine(t *testing.T) {
	if got := ntHash("password"); got != "8846F7EAEE8FB117AD06BDD830B7586C" {
		t.Fatalf("ntHash(password) = %s", got)
	}
	line := smbPasswdLine("plug", 1000, "password", time.Unix(0x5F000000, 0))
	want := "plug:1000:" + strings.Repeat("X", 32) + ":8846F7EAEE8FB117AD06BDD830B7586C:[U          ]:LCT-5F000000:\n"
	if line != want {
		t.Fatalf("line = %q\nwant %q", line, want)
	}
}

// The account helpers read passwd/group files without touching the system.
func TestPasswdAndGroupLookups(t *testing.T) {
	passwd := []byte("root:x:0:0:root:/root:/bin/sh\nplug:x:1000:1000::/:/sbin/nologin\n")
	if !passwdHas(passwd, "plug") || !passwdHas(passwd, "root") || passwdHas(passwd, "plu") || passwdHas(passwd, "nobody") {
		t.Fatal("passwdHas")
	}
	group := []byte("root:x:0:\nusers:x:100:\napp:x:1000:\n")
	if groupByGID(group, 1000) != "app" || groupByGID(group, 100) != "users" || groupByGID(group, 7) != "" {
		t.Fatal("groupByGID")
	}
}

// The image the helper runs is the agent's own unless an embedder overrides
// it - the same rule as the signpost.
func TestMountImage(t *testing.T) {
	t.Setenv(mountImageEnv, "")
	if mountImage("softwarity/plug:2.20.0") != "softwarity/plug:2.20.0" {
		t.Fatal("default is the agent's image")
	}
	t.Setenv(mountImageEnv, "  gateway/plug:mount  ")
	if mountImage("softwarity/plug:2.20.0") != "gateway/plug:mount" {
		t.Fatal("the override wins, trimmed")
	}
}

// volumes-of: what a workload mounts as DATA, in one line - never a tmpfs,
// never the secrets mount (files-of's), sorted; and the empty answer.
func TestDataVolumePathsAndReply(t *testing.T) {
	mounts := []dockerMount{
		{Type: "volume", Name: "pg", Destination: "/var/lib/postgresql/data"},
		{Type: "bind", Source: "/srv/x", Destination: "/data"},
		{Type: "tmpfs", Destination: "/tmp"},
		{Type: "bind", Source: "/run/secrets/x", Destination: "/run/secrets"},
		{Type: "bind", Source: "/x", Destination: "/run/secrets/tkofile"},
		{Type: "bind", Source: "/y", Destination: "/with space"},
		{Type: "bind", Source: "/etc", Destination: "/host-etc"}, // the helper would refuse it
	}
	got := dataVolumePaths(mounts)
	if strings.Join(got, ",") != "/data,/var/lib/postgresql/data" {
		t.Fatalf("paths = %v", got)
	}
	if volumesReply(got) != "volumes /data /var/lib/postgresql/data" || volumesReply(nil) != "volumes" {
		t.Fatalf("reply = %q / %q", volumesReply(got), volumesReply(nil))
	}
	var answered string
	old := answer
	answer = func(format string, a ...any) { answered = fmt.Sprintf(format, a...); panic("answered") }
	defer func() { answer = old }()
	func() { defer func() { recover() }(); dispatch([]string{"volumes-of", "Bad Name"}) }()
	if !strings.HasPrefix(answered, "error: ") {
		t.Fatalf("a bad name must be refused: %q", answered)
	}
}

// The Service in front of a Kubernetes helper: plug's own (the Service sweep
// reaps it with a dead session's names), a mount's, selecting the one pod by
// the helper label the pod carries.
func TestK8sMountServiceShape(t *testing.T) {
	svc := k8sMountService("shop", "plug-mnt-web-abcd1234", "10.1.2.3:40001")
	meta := svc["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]string)
	if labels[k8sManaged] != "plug" || labels[mountLabel] != "1" {
		t.Fatalf("labels %v", labels)
	}
	if meta["annotations"].(map[string]string)[sessionOwnerLabel] != "10.1.2.3:40001" {
		t.Fatal("the owner annotation")
	}
	spec := svc["spec"].(map[string]any)
	if spec["selector"].(map[string]string)[mountHelperLabel] != "plug-mnt-web-abcd1234" {
		t.Fatalf("selector %v", spec["selector"])
	}
	pod := k8sMountPod("shop", "plug-mnt-web-abcd1234", "web", "/data", "c", "", "img", "o", "p", nil)
	if pod["metadata"].(map[string]any)["labels"].(map[string]string)[mountHelperLabel] != "plug-mnt-web-abcd1234" {
		t.Fatal("the pod must carry the label the Service selects")
	}
}

// mount-status: one line, the orchestrator's words after the state.
func TestMountStatusLine(t *testing.T) {
	if statusLine("running", "") != "status running" || statusLine("pending", "no suitable node") != "status pending: no suitable node" {
		t.Fatal("statusLine")
	}
	var answered string
	old := answer
	answer = func(format string, a ...any) { answered = fmt.Sprintf(format, a...); panic("answered") }
	defer func() { answer = old }()
	for _, c := range [][]string{{"mount-status"}, {"mount-status", "web", "data"}, {"mount-status", "Web", "data", "40000"}, {"mount-status", "web", "data", "x"}} {
		answered = ""
		func() { defer func() { recover() }(); dispatch(c) }()
		if !strings.HasPrefix(answered, "error: ") {
			t.Errorf("%v: expected a refusal, got %q", c, answered)
		}
	}
}
