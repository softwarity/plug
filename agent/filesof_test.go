package agent

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The mounts worth projecting are the secret/configMap/projected ones, and the
// ServiceAccount token the kubelet injects into every pod is NOT one: its token
// and API CA are the cluster's own credentials, useless outside and a needless
// thing to copy to a laptop - dropped exactly as envExcluded drops KUBERNETES_*.
func TestProjectableMountsKeepsSecretsAndDropsServiceAccount(t *testing.T) {
	vols := []k8sVolume{
		{Name: "tls", Secret: json.RawMessage(`{"secretName":"canopydb-tls"}`)},
		{Name: "cfg", ConfigMap: json.RawMessage(`{"name":"app-cfg"}`)},
		{Name: "kube-api-access", Projected: json.RawMessage(`{"sources":[{"serviceAccountToken":{}}]}`)},
		{Name: "scratch"}, // emptyDir: no content to project
	}
	mounts := []k8sMount{
		{Name: "tls", MountPath: "/certificates"},
		{Name: "cfg", MountPath: "/etc/app"},
		{Name: "kube-api-access", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"},
		{Name: "scratch", MountPath: "/tmp/work"},
	}
	got := projectableMounts(vols, mounts)
	want := []string{"/certificates", "/etc/app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A projected volume mounted somewhere OTHER than the SA path is real app
// config and is kept; the SA exclusion is by mount path, not by volume kind.
func TestProjectableMountsKeepsAppProjectedVolume(t *testing.T) {
	vols := []k8sVolume{{Name: "conf", Projected: json.RawMessage(`{"sources":[{"configMap":{"name":"c"}}]}`)}}
	mounts := []k8sMount{{Name: "conf", MountPath: "/etc/conf"}}
	if got := projectableMounts(vols, mounts); !reflect.DeepEqual(got, []string{"/etc/conf"}) {
		t.Fatalf("a projected app-config mount must be kept, got %v", got)
	}
}

// The reply is JSON the client can read: the paths, and the tar base64'd.
func TestFilesReplyJSONRoundTrips(t *testing.T) {
	s := filesReplyJSON([]string{"/certificates"}, []byte("TARBYTES"), nil)
	var r filesReply
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Paths, []string{"/certificates"}) {
		t.Fatalf("paths = %v", r.Paths)
	}
	raw, err := base64.StdEncoding.DecodeString(r.Tar)
	if err != nil || string(raw) != "TARBYTES" {
		t.Fatalf("tar = %q err %v", raw, err)
	}
	// No files: paths is an empty array (not null), tar empty - the client reads
	// that as nothing to materialise.
	var empty filesReply
	json.Unmarshal([]byte(filesReplyJSON(nil, nil, nil)), &empty)
	if empty.Paths == nil || len(empty.Paths) != 0 || empty.Tar != "" {
		t.Fatalf("empty reply = %+v", empty)
	}
}

// rerootTar puts a Docker archive's members (rooted at the path's basename) back
// on their absolute path minus the leading slash - the shape the client's untar
// re-anchors under its temp dir, matching what `tar cf - /p` produces on k8s.
func TestRerootTarPutsMembersOnTheAbsolutePath(t *testing.T) {
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	// docker archive of /run/secrets names members "secrets/…"
	tw.WriteHeader(&tar.Header{Name: "secrets/tls.crt", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("PEM"))
	tw.Close()

	got := namesIn(t, rerootTar(in.Bytes(), "/run/secrets"))
	if !reflect.DeepEqual(got, []string{"run/secrets/tls.crt"}) {
		t.Fatalf("rerooted names = %v, want run/secrets/tls.crt", got)
	}
}

// tarFromFiles builds a tar whose members are the absolute paths (leading slash
// dropped), the Swarm path where the agent holds the bytes directly.
func TestTarFromFilesUsesAbsolutePaths(t *testing.T) {
	tb := tarFromFiles(map[string][]byte{"/certs/ca.crt": []byte("PEM")})
	got := namesIn(t, tb)
	if !reflect.DeepEqual(got, []string{"certs/ca.crt"}) {
		t.Fatalf("names = %v, want certs/ca.crt", got)
	}
	// And the content round-trips.
	tr := tar.NewReader(bytes.NewReader(tb))
	h, _ := tr.Next()
	b := make([]byte, h.Size)
	io.ReadFull(tr, b)
	if string(b) != "PEM" {
		t.Fatalf("content = %q", b)
	}
}

func namesIn(t *testing.T, tb []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(tb))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		out = append(out, h.Name)
	}
	return out
}

// mergeTars combines a Swarm reply's configs (built in-agent) with its secrets
// (stashed at park), each a tar, into the one tar files-of returns.
func TestMergeTarsCombinesBoth(t *testing.T) {
	a := tarFromFiles(map[string][]byte{"/etc/cfg/app.conf": []byte("CFG")})
	b := tarFromFiles(map[string][]byte{"/run/secrets/tls": []byte("SECRET")})
	got := namesIn(t, mergeTars(a, b))
	want := map[string]bool{"etc/cfg/app.conf": true, "run/secrets/tls": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("merged names = %v, want both members", got)
	}
	// Either side empty is a no-op that returns the other's entries.
	if n := namesIn(t, mergeTars(a, nil)); len(n) != 1 || n[0] != "etc/cfg/app.conf" {
		t.Fatalf("merge with empty = %v", n)
	}
}

// The Swarm secret stash lives under the agent's state directory, one file per
// name, and the boot clear removes every one of them: each was written by a
// session of this container, and a restart took every session down.
func TestSwarmSecretStashUnderStateDirAndClearedAtBoot(t *testing.T) {
	old := swarmSecretStashDir
	swarmSecretStashDir = filepath.Join(t.TempDir(), "swarm-secrets")
	defer func() { swarmSecretStashDir = old }()

	p := swarmSecretStash("web")
	if filepath.Dir(p) != swarmSecretStashDir || filepath.Base(p) != "plug-secrets-web.tar" {
		t.Fatalf("stash path = %q", p)
	}
	if err := os.MkdirAll(swarmSecretStashDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("TAR"), 0o600); err != nil {
		t.Fatal(err)
	}
	clearSwarmSecretStashes()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the stash must be gone after the boot clear, stat err = %v", err)
	}
	// Nothing to clear is not an error either.
	clearSwarmSecretStashes()
}

// fakeRun answers ls and cat from a script: argv joined by spaces to stdout,
// or to the error the command fails with. Anything unscripted fails, so a
// command the reader should never have sent shows up as a note.
func fakeRun(out map[string]string, fail map[string]string) execFunc {
	return func(argv ...string) ([]byte, error) {
		cmd := strings.Join(argv, " ")
		if why, ok := fail[cmd]; ok {
			return nil, execFailure{why}
		}
		if o, ok := out[cmd]; ok {
			return []byte(o), nil
		}
		return nil, execFailure{"unscripted: " + cmd}
	}
}

// The kubelet's atomic-writer layout reads as plain files: the ..data link and
// the dated directory are skipped, each key is read through its link, a
// subdirectory (an item path with a slash) is walked, a subPath mount is the
// one file it is, and an empty key is a file all the same. A file that cannot
// be read, and a mount whose image has no ls (distroless), are each named with
// the command's own reason; the mount that gave nothing is not reported read.
func TestReadMountedFilesReadsTheKubeletLayoutWithLsAndCat(t *testing.T) {
	run := fakeRun(map[string]string{
		"ls -1ApL /etc/tls":            "..2026_10_23_10_07_20.1111153391/\n..data/\nca.crt\nempty\nsub/\ntls.key\n",
		"ls -1ApL /etc/tls/sub":        "nested.conf\n",
		"ls -1ApL /etc/app.conf":       "/etc/app.conf\n",
		"cat /etc/tls/ca.crt":          "PEM",
		"cat /etc/tls/empty":           "",
		"cat /etc/tls/sub/nested.conf": "k=v",
		"cat /etc/app.conf":            "app",
	}, map[string]string{
		"cat /etc/tls/tls.key": "cat: /etc/tls/tls.key: Permission denied",
		"ls -1ApL /etc/ca":     "exec: executable file not found in $PATH",
	})
	files, read, notes := readMountedFiles(run, []string{"/etc/tls", "/etc/app.conf", "/etc/ca"})
	want := map[string]string{"/etc/tls/ca.crt": "PEM", "/etc/tls/empty": "", "/etc/tls/sub/nested.conf": "k=v", "/etc/app.conf": "app"}
	if len(files) != len(want) {
		t.Errorf("files = %v, want %v", files, want)
	}
	for p, body := range want {
		if got, ok := files[p]; !ok || string(got) != body {
			t.Errorf("%s = %q (present %v), want %q", p, got, ok, body)
		}
	}
	if !reflect.DeepEqual(read, []string{"/etc/tls", "/etc/app.conf"}) {
		t.Errorf("read = %v, want the two mounts that gave files", read)
	}
	wantNotes := []string{
		"/etc/ca: not read (exec: executable file not found in $PATH)",
		"/etc/tls/tls.key: not read (cat: /etc/tls/tls.key: Permission denied)",
	}
	if !reflect.DeepEqual(notes, wantNotes) {
		t.Errorf("notes = %q\nwant %q", notes, wantNotes)
	}
}

// files-of on Kubernetes, end to end over the exec subresource: the pod's
// secret mount comes back as a tar of its files, and the ServiceAccount mount
// is never listed. The same pod with an image whose ls fails gives no files,
// no mount reported read, and a note that says why.
func TestK8sFilesOfReadsTheMountsThroughExec(t *testing.T) {
	seed := func(fa *fakeAPIServer) {
		fa.add("services", k8sObject("Service", "api", nil, nil, map[string]any{"selector": map[string]string{"app": "api"}}))
		pod := k8sObject("Pod", "api-0", map[string]string{"app": "api"}, nil, map[string]any{
			"volumes": []map[string]any{
				{"name": "ca", "secret": map[string]any{"secretName": "ca"}},
				{"name": "kube-api-access", "projected": map[string]any{"sources": []any{}}},
			},
			"containers": []map[string]any{{"name": "api", "volumeMounts": []map[string]any{
				{"name": "ca", "mountPath": "/etc/ca"},
				{"name": "kube-api-access", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount"},
			}}},
		})
		pod["status"] = map[string]any{"phase": "Running"}
		fa.add("pods", pod)
	}

	fa := newFakeAPIServer(t, testNS, testPodIP)
	seed(fa)
	fa.execAnswers("ls -1ApL /etc/ca", "..data/\nca.crt\n")
	fa.execAnswers("cat /etc/ca/ca.crt", "-----BEGIN CERTIFICATE-----\n")
	paths, tb, notes := k8sFilesOf(testNS, "api")
	if !reflect.DeepEqual(paths, []string{"/etc/ca"}) || len(notes) != 0 {
		t.Fatalf("paths = %v notes = %q, want /etc/ca and nothing to say", paths, notes)
	}
	tr := tar.NewReader(bytes.NewReader(tb))
	h, err := tr.Next()
	if err != nil || h.Name != "etc/ca/ca.crt" {
		t.Fatalf("first member = %v, %v; want etc/ca/ca.crt", h, err)
	}
	if body, _ := io.ReadAll(tr); string(body) != "-----BEGIN CERTIFICATE-----\n" {
		t.Fatalf("ca.crt = %q", body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("a second member, the ..data link or the token: %v", err)
	}

	fa = newFakeAPIServer(t, testNS, testPodIP)
	seed(fa)
	fa.execFails("ls -1ApL /etc/ca", "", "ls: /etc/ca: Cannot stat: Function not implemented")
	paths, tb, notes = k8sFilesOf(testNS, "api")
	if paths != nil || tb != nil {
		t.Fatalf("paths = %v, tar of %d bytes: a mount that gave nothing is not read", paths, len(tb))
	}
	if !reflect.DeepEqual(notes, []string{"/etc/ca: not read (ls: /etc/ca: Cannot stat: Function not implemented)"}) {
		t.Fatalf("notes = %q", notes)
	}
}

// What a failed command wrote is not its answer, and an empty output from one
// that succeeded is (an empty file read with cat); k8sExec, for the readers
// that need something, still refuses an empty one.
func TestK8sExecTellsAFailedCommandFromAnEmptyOne(t *testing.T) {
	fa := newFakeAPIServer(t, testNS, testPodIP)
	seedK8sWorkload(fa, nil, nil)
	fa.execFails("tar -c -f - /etc/tls", string(make([]byte, 1024)), "tar: /etc/tls: Cannot stat: Function not implemented")
	fa.execAnswers("cat /etc/empty", "")
	if out, _, err := k8sExecOutput(testNS, "db-0", "db", "tar", "-c", "-f", "-", "/etc/tls"); err == nil || out != nil ||
		!strings.Contains(err.Error(), "Function not implemented") {
		t.Fatalf("a failed tar: out of %d bytes, err %v", len(out), err)
	}
	if out, _, err := k8sExecOutput(testNS, "db-0", "db", "cat", "/etc/empty"); err != nil || len(out) != 0 {
		t.Fatalf("an empty file: out %q, err %v", out, err)
	}
	if _, _, err := k8sExec(testNS, "db-0", "db", "cat", "/etc/empty"); err == nil {
		t.Fatal("k8sExec took an empty output for an answer")
	}
}
