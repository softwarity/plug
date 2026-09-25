package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The end the fix turns on: a variable that names a mounted path is repointed at
// the local copy, so NODE_EXTRA_CA_CERTS resolves without writing the host's
// real /certificates. A variable that does not name a mount is left untouched.
func TestLocalizeFileEnvRepointsMountPaths(t *testing.T) {
	vars := []string{
		"NODE_EXTRA_CA_CERTS=/certificates/ca.crt",
		"PGSSLROOTCERT=/certificates/ca.crt",
		"PORT=8080",
		"CERT_DIR=/certificates",
	}
	got := localizeFileEnv(vars, []string{"/certificates"}, "/tmp/plug-files-x")
	want := []string{
		"NODE_EXTRA_CA_CERTS=/tmp/plug-files-x/certificates/ca.crt",
		"PGSSLROOTCERT=/tmp/plug-files-x/certificates/ca.crt",
		"PORT=8080",
		"CERT_DIR=/tmp/plug-files-x/certificates",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	// A prefix that is not a path boundary must NOT match (/certificates-backup
	// is not under /certificates).
	got = localizeFileEnv([]string{"X=/certificates-backup/x"}, []string{"/certificates"}, "/tmp/d")
	if got[0] != "X=/certificates-backup/x" {
		t.Fatalf("a non-boundary prefix must not be rewritten, got %v", got)
	}
	// No dir (files unavailable) leaves everything as it was.
	if got := localizeFileEnv(vars, []string{"/certificates"}, ""); !reflect.DeepEqual(got, vars) {
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
