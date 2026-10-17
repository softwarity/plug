package main

import (
	"os"
	"path/filepath"
	"testing"
)

// What an installer asks before it grants privilege: the bytes it wrote,
// under the label the release signed them for. Signed is installed; unsigned,
// signed for another label, altered, or signed by another key is not.
func TestVerifyReleaseAdmitsOnlyTheSignedBytes(t *testing.T) {
	priv, restore := testKey(t)
	defer restore()
	file := filepath.Join(t.TempDir(), "plug")
	if err := os.WriteFile(file, []byte("the released binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(file)
	if err != nil {
		t.Fatal(err)
	}
	good := signed(priv, "linux-amd64", sum)
	if code := verifyRelease([]string{file, "linux-amd64", good}); code != 0 {
		t.Fatalf("the signed binary was refused (exit %d)", code)
	}
	for name, args := range map[string][]string{
		"unsigned":             {file, "linux-amd64", ""},
		"signed for a driver":  {file, "wintun-amd64", good},
		"malformed signature":  {file, "linux-amd64", "not base64!"},
		"no such file":         {file + ".missing", "linux-amd64", good},
		"missing the argument": {file, "linux-amd64"},
	} {
		if code := verifyRelease(args); code == 0 {
			t.Errorf("%s: admitted", name)
		}
	}
	if err := os.WriteFile(file, []byte("the released binary, altered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := verifyRelease([]string{file, "linux-amd64", good}); code == 0 {
		t.Error("altered bytes were admitted under the original signature")
	}
}
