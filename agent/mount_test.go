package agent

import (
	"fmt"
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
		"softwarity/plug:2.20.0", "10.1.2.3:40001", "s3cret")
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
	if env[smbUserEnv] != "plug" || env[smbPassEnv] != "s3cret" || env[smbShareEnv] != mountShare {
		t.Fatalf("env %v", env)
	}
	// No node known: nothing pinned, the scheduler decides.
	free := k8sMountPod("shop", "h", "web", "data", "c", "", "img", "o", "p")
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
	c := smbConf("vol", "plug", "plug", true)
	for _, want := range []string{
		"[global]", "security = user", "passdb backend = smbpasswd", "server min protocol = SMB2_02",
		"smb ports = 445", "disable netbios = yes", "load printers = no",
		"vfs objects = fruit streams_xattr",
		"[vol]", "path = " + mountVolumePath, "read only = no", "valid users = plug", "force user = plug",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	if strings.Contains(smbConf("vol", "plug", "root", false), "fruit") {
		t.Error("no fruit module, no fruit configuration")
	}
	if !strings.Contains(smbConf("vol", "plug", "root", false), "force user = root") {
		t.Error("a root-owned volume is served as root")
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
	pod := k8sMountPod("shop", "plug-mnt-web-abcd1234", "web", "/data", "c", "", "img", "o", "p")
	if pod["metadata"].(map[string]any)["labels"].(map[string]string)[mountHelperLabel] != "plug-mnt-web-abcd1234" {
		t.Fatal("the pod must carry the label the Service selects")
	}
}
