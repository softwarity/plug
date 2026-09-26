package agent

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
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
	s := filesReplyJSON([]string{"/certificates"}, []byte("TARBYTES"))
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
	json.Unmarshal([]byte(filesReplyJSON(nil, nil)), &empty)
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
