package main

// The personal key a profile can carry: what gets written, what gets offered,
// and what happens to it when the profile it belongs to moves or goes.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newProfile puts a minimal profile on disk inside the sandboxed home.
func newProfile(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(plugDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath(name), []byte("host = cluster.example\nport = 2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The pair has to be usable by the thing that will actually use it: the SSH
// client. Writing a file that only ssh-keygen would accept is the failure this
// catches, and it would only show up against a real agent.
func TestTheGeneratedPairIsOneTheSSHClientCanUse(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	priv, pub := profileKeyPath("dev"), profileKeyPath("dev")+".pub"

	writeKeyPair("dev", priv, pub)
	setProfileKey("dev", "key", priv)

	pem, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatalf("the private key plug wrote does not parse: %v", err)
	}
	line, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	published, _, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		t.Fatalf("the public half is not an authorized_keys line: %v", err)
	}
	// The half handed to an operator must be the half plug signs with. A
	// mismatch enrols a key that can never authenticate anyone.
	if ssh.FingerprintSHA256(published) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("the published key is not the one the private half signs with")
	}
	if got := loadProfile("dev").key; got != priv {
		t.Fatalf("profile key = %q, want %q", got, priv)
	}
}

// A private key is the one file here that must not be readable by anyone else,
// and ~/.plug/keys is created by plug rather than inherited.
func TestThePrivateHalfIsNotReadableByOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes do not describe Windows ACLs")
	}
	sandboxHome(t)
	newProfile(t, "dev")
	writeKeyPair("dev", profileKeyPath("dev"), profileKeyPath("dev")+".pub")

	fi, err := os.Stat(profileKeyPath("dev"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("private key mode %04o, want no group or other bits", mode)
	}
	di, err := os.Stat(keysDir())
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("%s mode %04o, want no group or other bits", keysDir(), mode)
	}
}

// The comment is what an operator reads in a list of enrolled keys. Without it
// every entry says "ed25519" and nothing else.
func TestThePublishedKeyNamesTheProfileAndTheMachine(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	writeKeyPair("dev", profileKeyPath("dev"), profileKeyPath("dev")+".pub")

	line, err := os.ReadFile(profileKeyPath("dev") + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	_, comment, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(comment, "plug-dev@") {
		t.Errorf("comment = %q, want it to name the profile and the host", comment)
	}
	if strings.Count(string(line), "\n") != 1 {
		t.Errorf("the public half must be exactly one line, got %q", line)
	}
}

// Both keys, in that order, and never one alone. Offering only the personal key
// would make `plug keygen` cut you off from every cluster that does not check
// keys, which is every cluster until someone enrols it.
func TestAProfileWithAKeyOffersItAheadOfTheBuiltInOne(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	priv := profileKeyPath("dev")
	writeKeyPair("dev", priv, priv+".pub")

	keys := config{key: priv}.authKeys()
	if len(keys) != 2 {
		t.Fatalf("offered %d keys, want the personal one and the built-in one", len(keys))
	}
	mine, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(keys[0]) != string(mine) {
		t.Error("the personal key is not offered first")
	}
	if string(keys[1]) != string(embeddedKey) {
		t.Error("the built-in key is not offered as the fallback")
	}
}

func TestAProfileWithoutAKeyOffersOnlyTheBuiltInOne(t *testing.T) {
	sandboxHome(t)
	keys := config{}.authKeys()
	if len(keys) != 1 || string(keys[0]) != string(embeddedKey) {
		t.Fatalf("offered %d keys, want just the built-in one", len(keys))
	}
}

// The key path is built from a profile name the same way the profile path is,
// and for the same reason: plug may hold root while it writes there, and
// filepath.Join resolves "../.." rather than refusing it.
func TestAKeyPathCannotLeaveTheKeysDirectory(t *testing.T) {
	sandboxHome(t)
	for _, bad := range []string{
		"../../../etc/ssh/sshd_config.d/evil",
		"..",
		"../keys",
		"a/b",
		"",
	} {
		if err := checkProfileName(bad); err == nil {
			t.Errorf("checkProfileName(%q) accepted a name that walks out of ~/.plug", bad)
		}
	}
	// And a name that passes lands where it should.
	got := profileKeyPath("dev")
	if filepath.Dir(got) != keysDir() {
		t.Errorf("profileKeyPath(dev) = %q, want it inside %q", got, keysDir())
	}
}

// A renamed profile that still pointed at the old path would be fatal on the
// next connection: authKeys refuses to read a key that is not there.
func TestRenamingAProfileTakesItsKeyAlong(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "old")
	writeKeyPair("old", profileKeyPath("old"), profileKeyPath("old")+".pub")
	setProfileKey("old", "key", profileKeyPath("old"))
	before, err := os.ReadFile(profileKeyPath("old"))
	if err != nil {
		t.Fatal(err)
	}

	// The profile file itself moves first, exactly as cmdRenameProfile does it.
	if err := os.Rename(profilePath("old"), profilePath("new")); err != nil {
		t.Fatal(err)
	}
	renameProfileKeys("old", "new")

	if _, err := os.Stat(profileKeyPath("old")); !os.IsNotExist(err) {
		t.Error("the old key is still there")
	}
	after, err := os.ReadFile(profileKeyPath("new"))
	if err != nil {
		t.Fatalf("the key did not follow the profile: %v", err)
	}
	if string(after) != string(before) {
		t.Error("the key changed on the way, so an enrolled public half stopped matching")
	}
	if got := loadProfile("new").key; got != profileKeyPath("new") {
		t.Errorf("the renamed profile points at %q, want %q", got, profileKeyPath("new"))
	}
	if _, err := os.Stat(profileKeyPath("new") + ".pub"); err != nil {
		t.Errorf("the public half did not follow: %v", err)
	}
}

// Removing a profile removes the identity it named. Leaving the private key
// behind would keep a credential on disk that nothing points at any more.
func TestRemovingAProfileRemovesItsKey(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	writeKeyPair("dev", profileKeyPath("dev"), profileKeyPath("dev")+".pub")

	removeProfileKeys("dev")

	for _, p := range []string{profileKeyPath("dev"), profileKeyPath("dev") + ".pub"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the profile it belonged to", p)
		}
	}
	// And doing it again on a profile that never had one is quiet.
	removeProfileKeys("never-had-one")
}

// Rotation replaces the pair; it does not merge with what was there. The old
// public half must stop being what plug signs with, or a revoked key would keep
// working.
func TestRenewReplacesTheWholePair(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	priv, pub := profileKeyPath("dev"), profileKeyPath("dev")+".pub"

	writeKeyPair("dev", priv, pub)
	first, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	writeKeyPair("dev", priv, pub)
	second, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("regenerating produced the same public key")
	}
	pem, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	published, _, _, _, err := ssh.ParseAuthorizedKey(second)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(published) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("after rotation the two halves no longer match")
	}
	// No temporary file left behind by the atomic install.
	entries, err := os.ReadDir(keysDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".plug-key-") {
			t.Errorf("a temporary key file was left in %s: %s", keysDir(), e.Name())
		}
	}
}

// `key = ...` has to survive a profile edited by hand, and be read back exactly
// as written: it is a path, and a mangled one is a fatal error at connect time.
func TestTheKeySettingSurvivesARoundTrip(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "dev")
	want := profileKeyPath("dev")

	setProfileKey("dev", "key", want)
	setProfileKey("dev", "update", "notify") // another setting written after it

	cfg := loadProfile("dev")
	if cfg.key != want {
		t.Errorf("key = %q, want %q", cfg.key, want)
	}
	if cfg.host != "cluster.example" || cfg.updateMode != "notify" {
		t.Errorf("the rest of the profile was disturbed: %+v", cfg)
	}
}
