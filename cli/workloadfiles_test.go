package main

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The end the fix turns on: a variable that names a mounted path is repointed at
// the local copy, so NODE_EXTRA_CA_CERTS resolves without writing the host's
// real /certificates. A variable that does not name a mount is left untouched,
// and one that names a file that was NOT brought over keeps the pod's path and
// is said: repointed, it would name a file that does not exist either.
func TestLocalizeFileEnvRepointsMountPaths(t *testing.T) {
	vars := []string{
		"NODE_EXTRA_CA_CERTS=/certificates/ca.crt",
		"PGSSLROOTCERT=/certificates/ca.crt",
		"PORT=8080",
		"CERT_DIR=/certificates",
		"TLS_KEY=/certificates/tls.key",
	}
	// The local paths are OS-native (a Windows dev machine gets backslashes),
	// so the expectations are built with filepath.Join, not hard-coded slashes.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "certificates"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "certificates", "ca.crt"), []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, missing := localizeFileEnv(vars, []string{"/certificates"}, dir)
	want := []string{
		"NODE_EXTRA_CA_CERTS=" + filepath.Join(dir, "/certificates/ca.crt"),
		"PGSSLROOTCERT=" + filepath.Join(dir, "/certificates/ca.crt"),
		"PORT=8080",
		"CERT_DIR=" + filepath.Join(dir, "/certificates"),
		"TLS_KEY=/certificates/tls.key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(missing, []string{"TLS_KEY"}) {
		t.Fatalf("missing = %v, want the variable whose file is not there", missing)
	}
	// A prefix that is not a path boundary must NOT match (/certificates-backup
	// is not under /certificates): the value is left exactly as it was.
	got, _ = localizeFileEnv([]string{"X=/certificates-backup/x"}, []string{"/certificates"}, dir)
	if got[0] != "X=/certificates-backup/x" {
		t.Fatalf("a non-boundary prefix must not be rewritten, got %v", got)
	}
	// No dir (files unavailable) leaves everything as it was.
	if got, _ := localizeFileEnv(vars, []string{"/certificates"}, ""); !reflect.DeepEqual(got, vars) {
		t.Fatalf("no dir must be a no-op, got %v", got)
	}
}

// untar writes the members under dir, and a member that tries to escape (an
// absolute path or a "..") lands inside dir all the same.
func TestUntarStaysUnderDirAndWritesContent(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	write("certificates/ca.crt", "PEMDATA")
	write("../escape.txt", "NOPE") // must be clamped under dir
	tw.Close()

	dir := t.TempDir()
	if err := untar(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "certificates", "ca.crt"))
	if err != nil || string(b) != "PEMDATA" {
		t.Fatalf("ca.crt = %q err %v", b, err)
	}
	// The escaping member landed inside dir (as escape.txt), never in the parent.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("a ../ member escaped dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatalf("the ../ member should have been clamped under dir: %v", err)
	}
}

// fetchWorkloadFiles materialises the workload's mounted secrets under a
// session directory from a tar the agent built. The tar comes from the
// cluster, so where its members land is a security property, not a detail.

// tarMember is one entry to put in a test tar.
type tarMember struct {
	name, body string
	typ        byte
	link       string
}

func filesOfReply(t *testing.T, paths []string, members ...tarMember) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o600, Size: int64(len(m.body)), Typeflag: m.typ, Linkname: m.link}
		if m.typ == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.Close()
	reply, _ := json.Marshal(map[string]any{"paths": paths, "tar": base64.StdEncoding.EncodeToString(buf.Bytes())})
	return string(reply)
}

// sandboxTemp points os.MkdirTemp at a directory of this test's own, on
// every OS, so the session directory can be found and is cleaned up.
func sandboxTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

// The files land under a fresh session directory, at their mount path, the
// directory private to the user, and the mount paths come back as the agent
// named them, which is what the variables are repointed with.
func TestFetchWorkloadFilesExtractsUnderTheSessionDirectory(t *testing.T) {
	tmp := sandboxTemp(t)
	f := &fakeExec{replies: map[string]string{
		"files-of api": filesOfReply(t, []string{"/etc/secret"},
			tarMember{name: "etc/secret/", typ: tar.TypeDir},
			tarMember{name: "etc/secret/ca.pem", body: "-----BEGIN CERTIFICATE-----\n"},
			tarMember{name: "etc/secret/token", body: "t0k3n"}),
	}}
	dir, paths, err := fetchWorkloadFiles(f, "api")
	if err != nil {
		t.Fatalf("fetchWorkloadFiles: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), "plug-files-") {
		t.Errorf("session directory %s is not a fresh plug-files-* under %s", dir, tmp)
	}
	if len(paths) != 1 || paths[0] != "/etc/secret" {
		t.Errorf("paths = %v, want the agent's", paths)
	}
	for name, want := range map[string]string{"ca.pem": "-----BEGIN CERTIFICATE-----\n", "token": "t0k3n"} {
		got, err := os.ReadFile(filepath.Join(dir, "etc", "secret", name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("the session directory holds secrets and is %v, want 0700", fi.Mode().Perm())
		}
	}
	// The variables that name the mount path now resolve here.
	vars, _ := localizeFileEnv([]string{"CA_FILE=/etc/secret/ca.pem"}, paths, dir)
	if want := "CA_FILE=" + filepath.Join(dir, "etc", "secret", "ca.pem"); vars[0] != want {
		t.Errorf("repointed to %q, want %q", vars[0], want)
	}
}

// A member that climbs out (../) or starts at the root is forced back under
// the session directory: the tar is the cluster's word, not this machine's,
// and nothing in it may write outside the directory made for it.
func TestFetchWorkloadFilesKeepsEveryMemberInsideTheDirectory(t *testing.T) {
	tmp := sandboxTemp(t)
	f := &fakeExec{replies: map[string]string{
		"files-of api": filesOfReply(t, []string{"/etc/secret"},
			tarMember{name: "etc/secret/ca.pem", body: "PEM"},
			tarMember{name: "../escaped", body: "x"},
			tarMember{name: "/abs/rooted", body: "y"},
			tarMember{name: "etc/../../climbed", body: "z"}),
	}}
	dir, _, err := fetchWorkloadFiles(f, "api")
	if err != nil {
		t.Fatalf("fetchWorkloadFiles: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, outside := range []string{filepath.Join(tmp, "escaped"), filepath.Join(tmp, "climbed"), filepath.Join(tmp, "abs", "rooted")} {
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("%s was written OUTSIDE the session directory", outside)
		}
	}
	for _, inside := range []string{filepath.Join(dir, "escaped"), filepath.Join(dir, "abs", "rooted"), filepath.Join(dir, "climbed")} {
		if _, err := os.Stat(inside); err != nil {
			t.Errorf("%s is not where the member was forced to: %v", inside, err)
		}
	}
}

// A symlink member is the one shape that could still point out of the tree
// once the names are clean: it is skipped, not created.
func TestFetchWorkloadFilesSkipsLinks(t *testing.T) {
	sandboxTemp(t)
	f := &fakeExec{replies: map[string]string{
		"files-of api": filesOfReply(t, []string{"/etc/secret"},
			tarMember{name: "etc/secret/passwd", typ: tar.TypeSymlink, link: filepath.Join(string(filepath.Separator), "etc", "passwd")},
			tarMember{name: "etc/secret/ok", body: "fine"}),
	}}
	dir, _, err := fetchWorkloadFiles(f, "api")
	if err != nil {
		t.Fatalf("fetchWorkloadFiles: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if _, err := os.Lstat(filepath.Join(dir, "etc", "secret", "passwd")); err == nil {
		t.Error("a symlink member was created")
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "secret", "ok")); err != nil {
		t.Errorf("the regular member beside it was not: %v", err)
	}
}

// Nothing mounted, an agent before the verb, an agent that refuses: each is
// told apart. The first two are not errors (the session runs as it always
// did), and none of them leaves a directory behind.
func TestFetchWorkloadFilesWithNothingToFetch(t *testing.T) {
	tmp := sandboxTemp(t)
	empty, _ := json.Marshal(map[string]any{"paths": []string{}, "tar": ""})
	for _, c := range []struct{ name, reply, wantErr string }{
		{"nothing mounted", string(empty), ""},
		{"old agent", "error: unknown command \"files-of\"", ""},
		{"refused", "error: files-of api: forbidden by the agent's policy", "forbidden"},
		{"garbage", "not json at all", ""},
	} {
		f := &fakeExec{replies: map[string]string{"files-of api": c.reply}}
		dir, paths, err := fetchWorkloadFiles(f, "api")
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: err = %v, want none", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
		if dir != "" || paths != nil {
			t.Errorf("%s: dir=%q paths=%v, want nothing", c.name, dir, paths)
		}
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("a directory was left behind with nothing to put in it: %v", entries)
	}
}

// An unreadable tar fails, and the directory made for it is removed: half a
// secret tree is worse than none, since the variables would be repointed at it.
func TestFetchWorkloadFilesRemovesTheDirectoryOnABrokenTar(t *testing.T) {
	tmp := sandboxTemp(t)
	reply, _ := json.Marshal(map[string]any{"paths": []string{"/etc/secret"},
		"tar": base64.StdEncoding.EncodeToString([]byte("this is not a tar archive, not even close to one"))})
	f := &fakeExec{replies: map[string]string{"files-of api": string(reply)}}
	if dir, _, err := fetchWorkloadFiles(f, "api"); err == nil {
		t.Fatalf("a broken tar was accepted, dir = %s", dir)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("the session directory survived the failed extraction: %v", entries)
	}
}

// What did not land is said and left out: the agent's notes (a file it could
// not read, a mount whose image has no ls), and a mount it announced but sent
// nothing for, which an agent before the ls/cat reader did when the image's
// tar failed (a valid, empty archive). Only the mount that landed comes back,
// so nothing is repointed at, or mounted from, an empty directory; and when
// none did, no directory is left behind.
func TestFetchWorkloadFilesReturnsOnlyWhatLanded(t *testing.T) {
	tmp := sandboxTemp(t)
	reply := filesOfReply(t, []string{"/etc/tls", "/etc/ca"},
		tarMember{name: "etc/tls/tls.crt", body: "PEM"})
	var r map[string]any
	json.Unmarshal([]byte(reply), &r)
	r["notes"] = []string{"/etc/key: not read (exec: executable file not found in $PATH)"}
	b, _ := json.Marshal(r)
	f := &fakeExec{replies: map[string]string{"files-of api": string(b)}}
	said := captureStderr(t)
	dir, paths, err := fetchWorkloadFiles(f, "api")
	out := said()
	if err != nil {
		t.Fatalf("fetchWorkloadFiles: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if !reflect.DeepEqual(paths, []string{"/etc/tls"}) {
		t.Errorf("paths = %v, want only the mount that landed", paths)
	}
	for _, want := range []string{
		"api: mounted file /etc/key: not read (exec: executable file not found in $PATH)",
		"api: the agent sent nothing for /etc/ca",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("not said: %q\nin: %s", want, out)
		}
	}

	// The old agent's failed tar: the mount announced, an empty archive.
	f = &fakeExec{replies: map[string]string{"files-of api": filesOfReply(t, []string{"/etc/ca"})}}
	said = captureStderr(t)
	dir, paths, err = fetchWorkloadFiles(f, "api")
	out = said()
	if err != nil || dir != "" || paths != nil {
		t.Fatalf("dir=%q paths=%v err=%v, want nothing", dir, paths, err)
	}
	if !strings.Contains(out, "api: the agent sent nothing for /etc/ca") {
		t.Errorf("the empty mount was not said: %s", out)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 1 {
		t.Errorf("a directory was left behind for the empty archive: %v", entries)
	}
}
