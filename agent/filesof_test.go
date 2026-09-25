package agent

import (
	"encoding/base64"
	"encoding/json"
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
